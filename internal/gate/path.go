package gate

import (
	"crypto/sha256"
	"encoding/hex"
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
func checkPath(u *url.URL) string {
	p := u.Path // decoded once by net/http
	if p == "" || p[0] != '/' {
		return "path must start with /"
	}
	raw := strings.ToLower(u.EscapedPath())
	for _, s := range []string{"%2f", "%5c", "%25"} {
		if strings.Contains(raw, s) {
			return "encoded slash, backslash or percent sign in path"
		}
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return "control character or backslash in path"
		}
	}
	if c := path.Clean(p); c != p && c+"/" != p {
		return "path is not in canonical form (// or dot segments)"
	}
	return ""
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

// hashResource is what tokens and stored offers keep instead of the
// resource itself, so their size does not depend on the URL.
func hashResource(res string) string {
	h := sha256.Sum256([]byte(res))
	return hex.EncodeToString(h[:16])
}
