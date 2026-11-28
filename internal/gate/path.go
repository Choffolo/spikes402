package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// MaxURIBytes is the longest request target (path and query) accepted.
// Longer requests get 414 URI Too Long: the URI ends up in tokens, logs
// and stored offers, so it must stay small.
const MaxURIBytes = 8 << 10

// checkPath refuses request paths that the origin could read differently
// from us, so that a protected page cannot be reached under another
// spelling. It returns a short reason, or "" if the path is fine.
// protected reports whether a path is behind the paywall or has a price
// rule, and nearLogin
// whether it is on or near a login route (either may be nil); they are
// used only for %25 (an encoded percent) and for "#" and "?".
func checkPath(u *url.URL, protected, nearLogin func(string) bool) string {
	guarded := func(p string) bool {
		return (protected != nil && protected(p)) || (nearLogin != nil && nearLogin(p))
	}
	p := u.Path // decoded once by net/http
	if p == "" || p[0] != '/' {
		return "path must start with /"
	}
	raw := strings.ToLower(u.EscapedPath())
	for _, s := range []string{"%2f", "%5c"} {
		if strings.Contains(raw, s) {
			return "encoded slash or backslash in path"
		}
	}
	// %25 is only refused when a second decode would change whether the
	// path is protected, so a harmless %25 on a free path is not a 400
	// for the whole site. Encoded slashes stay forbidden everywhere.
	if strings.Contains(raw, "%25") && (protected != nil || nearLogin != nil) {
		if twice, err := url.PathUnescape(p); err == nil {
			oncePay := guarded(p)
			if guarded(twice) != oncePay || guarded(path.Clean(twice)) != oncePay || cutRisky(twice, protected, nearLogin) {
				return "encoded percent sign in path"
			}
			for i := 0; i < len(twice); i++ {
				if c := twice[i]; c < 0x20 || c == 0x7f || c == '\\' {
					return "encoded percent sign in path"
				}
			}
			if c := path.Clean(twice); c != twice && c+"/" != twice {
				return "encoded percent sign in path"
			}
		}
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return "control character or backslash in path"
		}
	}
	if cutRisky(p, protected, nearLogin) {
		return "# or ? in path"
	}
	if c := path.Clean(p); c != p && c+"/" != p {
		return "path is not in canonical form (// or dot segments)"
	}
	return ""
}

// cutRisky reports whether p holds a "#" (raw, which no browser sends, or
// %23) or "?" (%3F) where an origin that decodes and re-parses the
// target could cut it, on a path that is paid (protected or priced; see
// checkPath) or near a login route, before or after the cut:
// "/login%23x" could be served as "/login", and
// "/premium/report.pdf%23.jpg" priced as a ".jpg" but served as the PDF.
// Elsewhere "#" and "?" are left alone ("/wiki/C%23").
func cutRisky(p string, paid, nearLogin func(string) bool) bool {
	for i := 0; i < len(p); i++ {
		if p[i] != '#' && p[i] != '?' {
			continue
		}
		cut := p[:i]
		if nearLogin != nil && (nearLogin(cut) || nearLogin(p)) {
			return true
		}
		if paid != nil && (paid(cut) || paid(p)) {
			return true
		}
	}
	return false
}

// UsablePattern reports whether pat can match a request path after the
// gate normalises spellings. A trailing slash other than "/" would never
// match, because trailing slashes are stripped.
func UsablePattern(pat string, caseSensitive bool) error {
	if strings.HasSuffix(pat, "/") && pat != "/" {
		base := strings.TrimRight(pat, "/")
		if base == "" {
			base = "/"
		}
		return fmt.Errorf("must not end with a trailing slash (it would never match); use %q or %q", base, base+"/**")
	}
	core := pat
	if prefix, ok := strings.CutSuffix(pat, "/**"); ok {
		core = prefix
		if core == "" {
			return nil
		}
	}
	key := matchKey(core, caseSensitive)
	want := key
	if strings.HasSuffix(pat, "/**") {
		want = key + "/**"
	}
	if caseSensitive {
		if key != core {
			return fmt.Errorf("would never match a request path (the gate normalises it to %q)", want)
		}
	} else if key != strings.ToLower(core) {
		return fmt.Errorf("would never match a request path (the gate normalises it to %q)", want)
	}
	if !strings.HasSuffix(pat, "/**") && !matchPattern(pat, key) {
		return fmt.Errorf("would never match a request path (the gate normalises it to %q)", want)
	}
	return nil
}

// matchKey is the form of a path used to match the protect patterns. It
// undoes the variations that some web servers ignore, so that all of them
// match the same pattern:
//
//   - ";params" at the end of a segment (Tomcat, Spring, Jetty), including "..;"
//   - trailing dots and spaces in a segment (Windows, IIS)
//   - empty segments and a trailing slash
//   - letter case, unless caseSensitive (Express, IIS, Windows and macOS file systems)
func matchKey(p string, caseSensitive bool) string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if i := strings.IndexByte(s, ';'); i >= 0 {
			s = s[:i]
		}
		switch s {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			continue
		}
		if s = strings.TrimRight(s, " ."); s != "" {
			out = append(out, s)
		}
	}
	k := "/" + strings.Join(out, "/")
	if !caseSensitive {
		k = strings.ToLower(k)
	}
	return k
}

// matchPattern reports whether a protect/prices pattern matches a
// canonical path (already passed through matchKey). A pattern ending in
// "/**" matches that prefix and everything below it; anything else uses
// path.Match.
func matchPattern(pat, key string) bool {
	if prefix, ok := strings.CutSuffix(pat, "/**"); ok {
		return key == prefix || strings.HasPrefix(key, prefix+"/")
	}
	ok, _ := path.Match(pat, key)
	return ok
}

// hashResource is what tokens and stored offers keep instead of the
// resource itself, so their size does not depend on the URL.
func hashResource(res string) string {
	h := sha256.Sum256([]byte(res))
	return hex.EncodeToString(h[:16])
}

// PriceFor is the price of path under the given rules (first match wins).
// Letter case is ignored unless caseSensitive is set, like the gate itself.
func PriceFor(path string, prices []PriceRule, def int64, caseSensitive bool) int64 {
	key := matchKey(path, caseSensitive)
	for _, r := range prices {
		pat := r.Pattern
		if !caseSensitive {
			pat = strings.ToLower(pat)
		}
		if matchPattern(pat, key) {
			return r.PriceSats
		}
	}
	return def
}
