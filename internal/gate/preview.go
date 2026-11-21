package gate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Teaser modes for PreviewRule.Mode.
const (
	PreviewNone    = "none"    // nothing from the origin (the default)
	PreviewTitle   = "title"   // title and meta description
	PreviewExcerpt = "excerpt" // title, description and the first MaxChars of text
)

// Limits of the teaser. They are fixed on purpose: the preview is a
// deliberate leak of a few words, never a way to read a page for free.
const (
	DefaultPreviewChars = 300
	MaxPreviewChars     = 1000

	previewMaxBody  = 256 << 10 // bytes read from the origin
	previewMaxToken = 20000     // HTML tokens looked at
	previewTitleMax = 150       // runes
	previewDescMax  = 300       // runes
	previewTimeout  = 3 * time.Second
	previewTTL      = 5 * time.Minute // a page's teaser is reused this long
	previewFailTTL  = time.Minute     // so is "no teaser" (origin down, not HTML, ...)
	previewEntries  = 256             // cached paths
	previewBurst    = 2               // origin fetches at once
	previewPerMin   = 60              // origin fetches per minute, all pages together
)

// PreviewRule shows a teaser of the protected page on the 402 page. The
// teaser is the same for every visitor, human or bot.
type PreviewRule struct {
	Pattern  string `json:"pattern"` // same syntax as Protect; first match wins
	Mode     string `json:"mode"`    // none, title or excerpt
	MaxChars int    `json:"max_chars,omitempty"`
}

// Check reports what is wrong with a rule's mode and size.
func (r PreviewRule) Check() error {
	switch r.Mode {
	case PreviewNone, PreviewTitle, PreviewExcerpt:
	default:
		return fmt.Errorf(`"mode" must be %q, %q or %q`, PreviewNone, PreviewTitle, PreviewExcerpt)
	}
	if r.MaxChars < 0 || r.MaxChars > MaxPreviewChars {
		return fmt.Errorf(`"max_chars" must be between 0 and %d`, MaxPreviewChars)
	}
	if r.MaxChars != 0 && r.Mode != PreviewExcerpt {
		return fmt.Errorf(`"max_chars" only applies to mode %q`, PreviewExcerpt)
	}
	return nil
}

func (r PreviewRule) chars() int {
	if r.MaxChars == 0 {
		return DefaultPreviewChars
	}
	return r.MaxChars
}

// Preview is the teaser: plain text only (no markup survives), size-capped.
type Preview struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Excerpt     string `json:"excerpt,omitempty"`
}

// previewer fetches, extracts and caches teasers. Safe for concurrent use.
type previewer struct {
	rules         []PreviewRule
	caseSensitive bool
	next          http.Handler
	keep          []string // forwarding headers copied to the origin request
	now           func() time.Time

	sem chan struct{} // limits concurrent origin fetches

	mu       sync.Mutex
	cache    map[string]previewEntry
	inflight map[string]*previewCall
	window   time.Time // start of the current one-minute budget
	fetched  int
}

type previewEntry struct {
	p       *Preview // nil: no teaser
	at      time.Time
	expires time.Time
}

type previewCall struct {
	done chan struct{}
	p    *Preview
}

func newPreviewer(opt Options) *previewer {
	if len(opt.Previews) == 0 {
		return nil
	}
	keep := []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-IP", "Forwarded"}
	if opt.ClientIPHeader != "" {
		keep = append(keep, opt.ClientIPHeader)
	}
	return &previewer{
		rules:         append([]PreviewRule(nil), opt.Previews...),
		caseSensitive: opt.CaseSensitive,
		next:          opt.Next,
		keep:          keep,
		now:           time.Now,
		sem:           make(chan struct{}, previewBurst),
		cache:         map[string]previewEntry{},
		inflight:      map[string]*previewCall{},
	}
}

func (pv *previewer) rule(path string) (PreviewRule, bool) {
	key := matchKey(path, pv.caseSensitive)
	for _, r := range pv.rules {
		pat := r.Pattern
		if !pv.caseSensitive {
			pat = strings.ToLower(pat)
		}
		if matchPattern(pat, key) {
			return r, true
		}
	}
	return PreviewRule{}, false
}

// get returns the teaser for r's path, or nil. It never fails a request:
// any problem means "no teaser".
func (pv *previewer) get(r *http.Request) *Preview {
	if pv == nil || r.Method != http.MethodGet {
		return nil
	}
	rule, ok := pv.rule(r.URL.Path)
	if !ok || rule.Mode == PreviewNone {
		return nil
	}
	key := r.URL.EscapedPath() // the query string never reaches the origin
	now := pv.now()

	pv.mu.Lock()
	if e, ok := pv.cache[key]; ok && now.Before(e.expires) {
		pv.mu.Unlock()
		return e.p
	}
	if c, ok := pv.inflight[key]; ok { // someone is already fetching it
		pv.mu.Unlock()
		<-c.done
		return c.p
	}
	select {
	case pv.sem <- struct{}{}:
	default: // busy: no teaser this time, and do not remember that
		pv.mu.Unlock()
		return nil
	}
	if now.Sub(pv.window) >= time.Minute {
		pv.window, pv.fetched = now, 0
	}
	if pv.fetched >= previewPerMin {
		<-pv.sem
		pv.mu.Unlock()
		return nil
	}
	pv.fetched++
	c := &previewCall{done: make(chan struct{})}
	pv.inflight[key] = c
	pv.mu.Unlock()

	p := pv.fetch(r, rule)
	<-pv.sem

	pv.mu.Lock()
	delete(pv.inflight, key)
	ttl := previewTTL
	if p == nil {
		ttl = previewFailTTL
	}
	pv.store(key, previewEntry{p: p, at: now, expires: now.Add(ttl)})
	pv.mu.Unlock()
	c.p = p
	close(c.done)
	return p
}

// store adds an entry, evicting expired ones and then the oldest. mu held.
func (pv *previewer) store(key string, e previewEntry) {
	if _, ok := pv.cache[key]; !ok && len(pv.cache) >= previewEntries {
		var oldest string
		for k, v := range pv.cache {
			if !pv.now().Before(v.expires) {
				delete(pv.cache, k)
			} else if oldest == "" || v.at.Before(pv.cache[oldest].at) {
				oldest = k
			}
		}
		if len(pv.cache) >= previewEntries && oldest != "" {
			delete(pv.cache, oldest)
		}
	}
	pv.cache[key] = e
}

// fetch asks the origin for the page as an anonymous visitor would:
// GET, the path only, no cookies and no credentials.
func (pv *previewer) fetch(r *http.Request, rule PreviewRule) *Preview {
	// Not tied to the visitor's request: the result is shared and cached.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), previewTimeout)
	defer cancel()
	req := r.Clone(ctx)
	req.Method = http.MethodGet
	req.Body, req.GetBody, req.ContentLength = http.NoBody, nil, 0
	req.URL = &url.URL{Path: r.URL.Path, RawPath: r.URL.RawPath}
	req.RequestURI = req.URL.RequestURI()
	req.Header = http.Header{"Accept": {"text/html"}, "User-Agent": {"Spikes402-Preview"}}
	for _, name := range pv.keep {
		if v := r.Header.Values(name); len(v) > 0 {
			req.Header[http.CanonicalHeaderKey(name)] = append([]string(nil), v...)
		}
	}

	cw := &capWriter{h: http.Header{}, max: previewMaxBody}
	serveRecovered(pv.next, cw, req)
	if cw.status != http.StatusOK || !isHTML(cw.h.Get("Content-Type")) {
		return nil
	}
	return extractPreview(cw.buf.Bytes(), rule)
}

// serveRecovered calls h and swallows http.ErrAbortHandler, which a
// reverse proxy panics with when it gives up on a response.
func serveRecovered(h http.Handler, w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil && v != http.ErrAbortHandler {
			panic(v)
		}
	}()
	h.ServeHTTP(w, r)
}

func isHTML(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && (mt == "text/html" || mt == "application/xhtml+xml")
}

// capWriter keeps the first max bytes of a response and drops the rest.
// Headers (Set-Cookie included) stay in memory and are never forwarded.
type capWriter struct {
	h      http.Header
	status int
	buf    bytes.Buffer
	max    int
}

func (c *capWriter) Header() http.Header { return c.h }

func (c *capWriter) WriteHeader(code int) {
	if c.status == 0 && code >= 200 {
		c.status = code
	}
}

func (c *capWriter) Write(p []byte) (int, error) {
	c.WriteHeader(http.StatusOK)
	if room := c.max - c.buf.Len(); room > 0 {
		c.buf.Write(p[:min(len(p), room)])
	}
	if c.buf.Len() >= c.max {
		return len(p), errPreviewFull // stops a well-behaved copier early
	}
	return len(p), nil
}

var errPreviewFull = errors.New("preview: enough bytes")

// skipTags are elements whose text is never part of a teaser.
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true, "math": true,
	"nav": true, "header": true, "footer": true, "aside": true, "form": true, "button": true,
	"select": true, "textarea": true, "iframe": true, "object": true,
}

// extractPreview reads title, description and paragraphs from HTML. The
// result is plain text: markup is dropped, never escaped or rewritten.
func extractPreview(doc []byte, rule PreviewRule) *Preview {
	var title, ogTitle, desc, ogDesc string
	var paras []string
	var para strings.Builder
	var inTitle, inP bool
	skip, total := 0, 0
	maxChars := rule.chars()
	flush := func() { // end of a paragraph, closed or not
		if s := cleanText(para.String(), maxChars); inP && s != "" {
			paras = append(paras, s)
			total += len(s)
		}
		inP = false
		para.Reset()
	}

	z := html.NewTokenizer(bytes.NewReader(doc))
	for i := 0; i < previewMaxToken; i++ {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.TextToken:
			text := string(z.Text())
			switch {
			case inTitle:
				title += text
			case inP && skip == 0:
				para.WriteString(text)
				para.WriteByte(' ')
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			switch tag {
			case "title":
				inTitle = tt == html.StartTagToken && title == "" && skip == 0
			case "meta":
				var key, val string
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch string(k) {
					case "name", "property":
						key = strings.ToLower(string(v))
					case "content":
						val = string(v)
					}
				}
				switch key {
				case "description":
					if desc == "" {
						desc = val
					}
				case "og:title":
					if ogTitle == "" {
						ogTitle = val
					}
				case "og:description":
					if ogDesc == "" {
						ogDesc = val
					}
				}
			case "p":
				flush() // <p> may be left open
				inP = tt == html.StartTagToken && skip == 0
			case "br":
				para.WriteByte(' ')
			}
			if skipTags[tag] && tt == html.StartTagToken {
				skip++
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			switch {
			case tag == "title":
				inTitle = false
			case tag == "p":
				flush()
			case skipTags[tag] && skip > 0:
				skip--
			}
		}
		if rule.Mode == PreviewExcerpt && total >= maxChars {
			break
		}
	}

	p := &Preview{
		Title:       cleanText(first(title, ogTitle), previewTitleMax),
		Description: cleanText(first(desc, ogDesc), previewDescMax),
	}
	if rule.Mode == PreviewExcerpt {
		p.Excerpt = cleanText(strings.Join(paras, " "), maxChars)
	}
	if *p == (Preview{}) {
		return nil
	}
	return p
}

func first(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// tagLike matches text that looks like a tag. Entity-decoded text can
// contain one ("&lt;b&gt;"), and a title is raw text, so it can too.
var tagLike = regexp.MustCompile(`<[A-Za-z/!?][^>]*>`)

// cleanText returns s as one line of plain text of at most limit runes,
// cut at a word boundary with an ellipsis. Control, invisible and
// direction-changing characters and angle brackets are removed.
func cleanText(s string, limit int) string {
	s = tagLike.ReplaceAllString(strings.ToValidUTF8(s, ""), " ")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '<' || r == '>':
			return -1
		case unicode.IsSpace(r) || unicode.Is(unicode.Cc, r):
			return ' '
		case unicode.In(r, unicode.Cf, unicode.Cs, unicode.Co, unicode.Zl, unicode.Zp):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	cut := []rune(s)[:limit]
	if i := strings.LastIndexByte(string(cut), ' '); i > limit/2 {
		return strings.TrimRight(string(cut)[:i], " ,.;:-") + "…"
	}
	return strings.TrimRight(string(cut), " ,.;:-") + "…"
}
