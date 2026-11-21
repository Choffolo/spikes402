package gate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

const evilPage = `<!doctype html><html><head>
<title>Secret &amp; <b>Report</b></title>
<meta name="description" content="A &lt;script&gt;alert(1)&lt;/script&gt; teaser">
<script>var leak="SCRIPT-TEXT";</script><style>.x{}</style></head>
<body><nav><p>NAV-TEXT</p></nav>
<p>First &lt;img src=x onerror=alert(2)&gt; paragraph,&#8238; with<br>a break&#8203;.</p>
<form><p>FORM-TEXT</p></form>
<svg><title>SVG-TITLE</title><text>SVG-TEXT</text></svg>
<p>Second <a href="javascript:alert(3)">paragraph</a> <script>alert("INLINE")</script>of text.
<p>Third paragraph, left open
<footer><p>FOOTER-TEXT</p></footer></body></html>`

type originLog struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (l *originLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reqs = append(l.reqs, r)
}

func (l *originLog) n() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.reqs)
}

// previewFixture is a gate whose origin serves body as HTML.
func previewFixture(t *testing.T, body string, rules ...PreviewRule) (*fixture, *originLog) {
	t.Helper()
	f := newFixture(t)
	log := &originLog{}
	f.gate.opt.Next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		http.SetCookie(w, &http.Cookie{Name: "origin", Value: "1"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	})
	f.gate.preview = newPreviewer(Options{Previews: rules, Next: f.gate.opt.Next})
	return f, log
}

func excerptRule(n int) PreviewRule {
	return PreviewRule{Pattern: "/premium/**", Mode: PreviewExcerpt, MaxChars: n}
}

func TestExtractPreviewSanitizes(t *testing.T) {
	p := extractPreview([]byte(evilPage), excerptRule(0))
	if p == nil {
		t.Fatal("no preview")
	}
	all := p.Title + "|" + p.Description + "|" + p.Excerpt
	for _, bad := range []string{"<", ">", "SCRIPT-TEXT", "NAV-TEXT", "FORM-TEXT", "SVG-", "FOOTER-TEXT", "INLINE", "\u202e", "\u200b", "\n"} {
		if strings.Contains(all, bad) {
			t.Errorf("preview contains %q: %q", bad, all)
		}
	}
	if p.Title != "Secret & Report" {
		t.Errorf("title %q", p.Title)
	}
	if p.Description != "A alert(1) teaser" {
		t.Errorf("description %q", p.Description)
	}
	want := "First paragraph, with a break. Second paragraph of text. Third paragraph, left open"
	if p.Excerpt != want {
		t.Errorf("excerpt %q, want %q", p.Excerpt, want)
	}
}

func TestExtractPreviewModesAndCaps(t *testing.T) {
	long := "<title>T</title><body>" + strings.Repeat("<p>"+strings.Repeat("word ", 50)+"</p>", 40)
	if p := extractPreview([]byte(long), PreviewRule{Mode: PreviewTitle}); p == nil || p.Excerpt != "" || p.Title != "T" {
		t.Errorf("title mode: %+v", p)
	}
	for _, n := range []int{0, 1, 50, 300, MaxPreviewChars} {
		p := extractPreview([]byte(long), PreviewRule{Mode: PreviewExcerpt, MaxChars: n})
		want := n
		if n == 0 {
			want = DefaultPreviewChars
		}
		if c := utf8.RuneCountInString(p.Excerpt); c > want+1 || c < min(want, 20)-1 { // +1: the ellipsis
			t.Errorf("max_chars=%d: excerpt has %d runes", n, c)
		}
		if n >= 50 && !strings.HasSuffix(p.Excerpt, "…") {
			t.Errorf("max_chars=%d: no ellipsis on a cut text", n)
		}
	}
	// Long titles and descriptions are cut too.
	big := "<title>" + strings.Repeat("a ", 500) + "</title><meta name=description content='" + strings.Repeat("b ", 500) + "'>"
	p := extractPreview([]byte(big), PreviewRule{Mode: PreviewTitle})
	if utf8.RuneCountInString(p.Title) > previewTitleMax+1 || utf8.RuneCountInString(p.Description) > previewDescMax+1 {
		t.Errorf("title %d runes, description %d", len(p.Title), len(p.Description))
	}
	// Nothing to show is no preview, and og: tags are the fallback.
	if extractPreview([]byte("<html><body><div>x</div></body></html>"), excerptRule(0)) != nil {
		t.Error("expected nil")
	}
	og := extractPreview([]byte(`<meta property="og:title" content="OG"><meta property="og:description" content="OD">`), PreviewRule{Mode: PreviewTitle})
	if og == nil || og.Title != "OG" || og.Description != "OD" {
		t.Errorf("og fallback: %+v", og)
	}
	// Invalid UTF-8 never reaches the output.
	if p := extractPreview([]byte("<title>a\xff\xfeb</title>"), PreviewRule{Mode: PreviewTitle}); p == nil || !utf8.ValidString(p.Title) {
		t.Errorf("invalid UTF-8: %+v", p)
	}
}

func TestPreviewOnThe402Page(t *testing.T) {
	f, _ := previewFixture(t, evilPage, excerptRule(0))
	rec := f.get("/premium/article?secret=1", "", "text/html")
	body := rec.Body.String()
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d, want 402", rec.Code)
	}
	for _, want := range []string{
		"<title>Secret &amp; Report · 402 Payment Required",
		`<meta name="description" content="A alert(1) teaser">`,
		"<article>", "First paragraph, with a break",
		"This page costs", // the wall is still there
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, bad := range []string{"onerror=alert", "<img src=x", "<b>Report", "SCRIPT-TEXT", "javascript:alert", `alert("INLINE")`, "NAV-TEXT"} {
		if strings.Contains(body, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	if got := len(inlineScripts(body)); got != 3 {
		t.Errorf("%d executable inline scripts, want 3", got)
	}
	// The origin's cookies are not passed on to the visitor.
	for _, c := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(c, "origin=") {
			t.Errorf("origin cookie leaked: %s", c)
		}
	}
	// JSON-LD carries the same, escaped.
	if !strings.Contains(body, `"name":"Secret \u0026 Report"`) {
		t.Errorf("JSON-LD lacks the title: %s", body[:min(len(body), 900)])
	}
}

func TestPreviewInJSONForClients(t *testing.T) {
	f, _ := previewFixture(t, evilPage, excerptRule(60))
	var b challengeBody
	rec := f.get("/premium/article", "", "")
	if err := json.NewDecoder(rec.Body).Decode(&b); err != nil || rec.Code != http.StatusPaymentRequired {
		t.Fatal(err, rec.Code)
	}
	if b.Preview == nil || b.Preview.Title != "Secret & Report" || utf8.RuneCountInString(b.Preview.Excerpt) > 61 {
		t.Errorf("preview: %+v", b.Preview)
	}
	if len(rec.Header().Values("WWW-Authenticate")) != 2 {
		t.Error("machine-readable headers changed")
	}
	// No preview configured: no field at all.
	g := newFixture(t)
	if strings.Contains(g.get("/premium/article", "", "").Body.String(), `"preview"`) {
		t.Error("preview field without a rule")
	}
}

func TestPreviewIsTheSameForEveryone(t *testing.T) {
	f, _ := previewFixture(t, evilPage, excerptRule(0))
	var seen []string
	for _, ua := range []string{"Mozilla/5.0 (X11) Firefox", "GPTBot/1.2", "curl/8", ""} {
		req := httptest.NewRequest(http.MethodGet, "/premium/article", nil)
		req.Header.Set("User-Agent", ua)
		req.RemoteAddr = "203.0.113." + fmt.Sprint(len(seen)+1) + ":1234"
		rec := httptest.NewRecorder()
		f.gate.ServeHTTP(rec, req)
		var b challengeBody
		json.NewDecoder(rec.Body).Decode(&b)
		j, _ := json.Marshal(b.Preview)
		seen = append(seen, string(j))
	}
	for _, s := range seen {
		if s != seen[0] || s == "null" {
			t.Fatalf("previews differ: %q", seen)
		}
	}
}

func TestPreviewRequestToTheOrigin(t *testing.T) {
	f, log := previewFixture(t, evilPage, excerptRule(0))
	req := httptest.NewRequest(http.MethodGet, "/premium/article?token=secret", nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", "session=abc; spikes402-pass=x")
	req.Header.Set("Authorization", "PoW garbage") // rejected, then the 402 page with a preview
	req.Header.Set("Range", "bytes=0-5")
	req.Header.Set("If-None-Match", `"x"`)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if log.n() != 1 {
		t.Fatalf("origin got %d requests, want 1", log.n())
	}
	got := log.reqs[0]
	if got.Method != http.MethodGet || got.URL.RequestURI() != "/premium/article" || got.ContentLength != 0 {
		t.Errorf("request %s %s", got.Method, got.URL.RequestURI())
	}
	for _, h := range []string{"Cookie", "Authorization", "Range", "If-None-Match", "Spikes402-Redeem"} {
		if got.Header.Get(h) != "" {
			t.Errorf("%s reached the origin: %q", h, got.Header.Get(h))
		}
	}
	if got.Header.Get("Accept") != "text/html" || got.Header.Get("User-Agent") != "Spikes402-Preview" || got.Header.Get("X-Forwarded-Proto") != "https" {
		t.Errorf("headers: %v", got.Header)
	}
}

func TestPreviewCacheAndBudget(t *testing.T) {
	f, log := previewFixture(t, evilPage, excerptRule(0))
	now := time.Unix(1_800_000_000, 0)
	f.gate.preview.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		f.get("/premium/article?v="+fmt.Sprint(i), "", "")
	}
	if log.n() != 1 {
		t.Fatalf("origin hit %d times for one page, want 1 (cached)", log.n())
	}
	now = now.Add(previewTTL + time.Second)
	f.get("/premium/article", "", "")
	if log.n() != 2 {
		t.Fatalf("expired entry not refetched: %d", log.n())
	}
	base := log.n()
	// Many different pages: the origin sees at most previewPerMin of them.
	for i := 0; i < previewPerMin+40; i++ {
		f.get(fmt.Sprintf("/premium/page-%d", i), "", "")
	}
	if got := log.n() - base; got > previewPerMin-1 { // the refetch above used one
		t.Fatalf("origin hit %d times in a minute, want at most %d", got+1, previewPerMin)
	}
	now = now.Add(time.Minute)
	before := log.n()
	f.get("/premium/another", "", "")
	if log.n() != before+1 {
		t.Error("budget did not come back after a minute")
	}
}

func TestPreviewCacheIsBounded(t *testing.T) {
	f, _ := previewFixture(t, evilPage, excerptRule(0))
	now := time.Unix(1_800_000_000, 0)
	f.gate.preview.now = func() time.Time { return now }
	for i := 0; i < 3*previewEntries; i++ {
		now = now.Add(time.Second) // keeps the fetch budget refilling
		if i%50 == 0 {
			now = now.Add(time.Minute)
		}
		f.gate.preview.get(httptest.NewRequest(http.MethodGet, fmt.Sprintf("/premium/p%d", i), nil))
	}
	if n := len(f.gate.preview.cache); n > previewEntries {
		t.Fatalf("cache holds %d entries, cap is %d", n, previewEntries)
	}
}

func TestPreviewNoTeaser(t *testing.T) {
	rules := []PreviewRule{{Pattern: "/premium/secret/**", Mode: PreviewNone}, excerptRule(0)}
	f, log := previewFixture(t, evilPage, rules...)
	if rec := f.get("/premium/secret/a", "", ""); strings.Contains(rec.Body.String(), `"preview"`) || log.n() != 0 {
		t.Error(`mode "none" (first match wins) fetched or showed a teaser`)
	}
	if rec := f.get("/other.pdf", "", ""); strings.Contains(rec.Body.String(), `"preview"`) || log.n() != 0 {
		t.Error("a path without a rule fetched or showed a teaser")
	}
	// HEAD never fetches.
	req := httptest.NewRequest(http.MethodHead, "/premium/a", nil)
	f.gate.ServeHTTP(httptest.NewRecorder(), req)
	if log.n() != 0 {
		t.Error("HEAD fetched the origin")
	}
}

func TestPreviewOriginMisbehaves(t *testing.T) {
	for name, origin := range map[string]http.HandlerFunc{
		"not found": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(404)
			w.Write([]byte("<title>Not here</title>"))
		},
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", 302) },
		"json": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"title":"x"}`))
		},
		"no content type": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte{0}) },
		"panic":           func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) },
		"slow": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, _ := previewFixture(t, "", excerptRule(0))
			f.gate.preview.next = origin
			start := time.Now()
			rec := f.get("/premium/article", "", "")
			if rec.Code != http.StatusPaymentRequired || strings.Contains(rec.Body.String(), `"preview"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if time.Since(start) > previewTimeout+2*time.Second {
				t.Fatalf("took %v", time.Since(start))
			}
			// The failure is remembered for a short while: no hammering.
			n := 0
			f.gate.preview.next = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { n++ })
			f.get("/premium/article", "", "")
			if n != 0 {
				t.Error("failed fetch was retried at once")
			}
		})
	}
}

func TestPreviewBodyCap(t *testing.T) {
	f, _ := previewFixture(t, "", excerptRule(0))
	var written int
	f.gate.preview.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<title>Big</title><p>start of the page "))
		chunk := []byte(strings.Repeat("x", 1024))
		for i := 0; i < 100_000; i++ { // 100 MB if nobody stops us
			n, err := w.Write(chunk)
			written += n
			if err != nil {
				return
			}
		}
	})
	rec := f.get("/premium/article", "", "")
	if written > previewMaxBody+2048 {
		t.Fatalf("read %d bytes from the origin, cap is %d", written, previewMaxBody)
	}
	if !strings.Contains(rec.Body.String(), `"title": "Big"`) {
		t.Errorf("head of a huge page should still give a title: %s", rec.Body.String())
	}
}

func TestPreviewConcurrency(t *testing.T) {
	f, log := previewFixture(t, evilPage, excerptRule(0))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := f.get("/premium/article", "", "")
			if rec.Code != http.StatusPaymentRequired {
				t.Error(rec.Code)
			}
		}()
	}
	wg.Wait()
	if log.n() != 1 {
		t.Fatalf("origin hit %d times for 20 simultaneous visitors, want 1", log.n())
	}
}

func TestPreviewRuleCheck(t *testing.T) {
	for r, ok := range map[PreviewRule]bool{
		{Pattern: "/a", Mode: PreviewNone}:                    true,
		{Pattern: "/a", Mode: PreviewTitle}:                   true,
		{Pattern: "/a", Mode: PreviewExcerpt}:                 true,
		{Pattern: "/a", Mode: PreviewExcerpt, MaxChars: 1000}: true,
		{Pattern: "/a", Mode: PreviewExcerpt, MaxChars: 1001}: false,
		{Pattern: "/a", Mode: PreviewExcerpt, MaxChars: -1}:   false,
		{Pattern: "/a", Mode: PreviewTitle, MaxChars: 10}:     false,
		{Pattern: "/a", Mode: "full"}:                         false,
		{Pattern: "/a", Mode: ""}:                             false,
	} {
		if err := r.Check(); (err == nil) != ok {
			t.Errorf("%+v: %v", r, err)
		}
	}
}
