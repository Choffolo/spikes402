package gate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/Choffolo/spikes402/pow"
)

var scriptRE = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// inlineScripts are the bodies of the executable inline scripts of a page
// (data blocks such as ld+json have a type attribute and do not count).
func inlineScripts(body string) []string {
	var out []string
	for _, m := range scriptRE.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestEnergyWallPage(t *testing.T) {
	f := newFixture(t)
	rec := f.get("/premium/article", "", "text/html")
	body := rec.Body.String()
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d, want 402", rec.Code)
	}
	for _, want := range []string{
		"invisible energy wall built with Bitcoin",
		"Pay a few sats with the",
		"solve a math puzzle with your computer",
		`href="https://github.com/Choffolo/spikes402#browser-wallet-extension-prototype"`,
		`id="pow-go"`, `id="pow-bar"`, `id="pow-stats"`,
		`data-worker="` + workerSrc + `"`,
		`data-difficulty="8"`,
		`<div class="paywall">`,
		`class="ways"`,
		`width="160"`,
		`id="webln-pay"`, // the Lightning invoice flow is still there
		`id="open-wallet"`, `id="pay-wait"`, "Open in wallet",
		"data:image/png;base64,",
		`class="logo"`,
		`aria-hidden="true"`,
		`class="logo-link"`,
		`href="/"`,
		`aria-label="Back to the site"`,
		`rel="icon"`,
		`type="image/svg+xml"`,
		"data:image/svg", ";base64,",
		"#F7931A",
		"An invisible energy wall",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The challenge in the page is the one in the headers (one token per offer).
	_, token := weblnAttrs(t, body)
	if !strings.Contains(body, `data-challenge="`+token+`"`) {
		t.Error("puzzle and invoice use different tokens")
	}
	// Nothing from outside, nothing inline beyond the two hashed scripts.
	// The inline logo's xmlns is http://www.w3.org/2000/svg, not a fetch.
	if strings.Contains(body, "<script src") || strings.Contains(body, `src="http://`) || strings.Contains(body, `href="http://`) {
		t.Error("page loads or links something external")
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "img-src data:", "worker-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'unsafe") || strings.Contains(csp, "*") {
		t.Errorf("CSP too loose: %s", csp)
	}
	if got := len(inlineScripts(body)); got != 3 {
		t.Errorf("%d inline scripts, want 3 (wallet, invoice wait, puzzle)", got)
	}
	// Machine-readable parts are unchanged.
	if len(rec.Header().Values("WWW-Authenticate")) != 2 {
		t.Errorf("WWW-Authenticate: %q", rec.Header().Values("WWW-Authenticate"))
	}
}

func TestPowFirstWallPage(t *testing.T) {
	f := newFixture(t)
	f.gate.opt.WallMode = WallModePowFirst
	rec := f.get("/premium/article", "", "text/html")
	body := rec.Body.String()
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d, want 402", rec.Code)
	}
	for _, want := range []string{
		`data-autostart="1"`,
		"Tired of waiting? Pay this",
		`id="webln-pay"`,
		`data-invoice="`,
		`id="open-wallet"`,
		`id="pay-wait"`,
		"Open in wallet",
		"data:image/png;base64,",
		`id="pow-go"`,
		`data-challenge="`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pow_first page lacks %q", want)
		}
	}
	if strings.Contains(body, "Or pay with Lightning") {
		t.Error("pow_first page still uses the two-door heading")
	}
	invoice, token := weblnAttrs(t, body)
	if invoice == "" || !strings.Contains(body, `data-challenge="`+token+`"`) {
		t.Error("puzzle and invoice should still share the same offer")
	}
}

func TestDefaultWallPageHasNoAutostart(t *testing.T) {
	f := newFixture(t)
	body := f.get("/premium/article", "", "text/html").Body.String()
	if strings.Contains(body, `data-autostart="1"`) || strings.Contains(body, "Tired of waiting?") {
		t.Error("default wall should not autostart or use the pow_first caption")
	}
	if !strings.Contains(body, "Or pay with Lightning") || !strings.Contains(body, `id="webln-pay"`) {
		t.Error("default wall should keep the two-door layout and invoice button")
	}
}

func TestPaywallMarkup(t *testing.T) {
	f := newFixture(t)
	body := f.get("/premium/article", "", "text/html").Body.String()
	m := regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no JSON-LD")
	}
	var ld struct {
		Type    string `json:"@type"`
		Free    bool   `json:"isAccessibleForFree"`
		HasPart struct {
			Type string `json:"@type"`
			Free bool   `json:"isAccessibleForFree"`
			Sel  string `json:"cssSelector"`
		} `json:"hasPart"`
	}
	if err := json.Unmarshal([]byte(m[1]), &ld); err != nil {
		t.Fatal(err)
	}
	if ld.Type != "WebPage" || ld.Free || ld.HasPart.Type != "WebPageElement" || ld.HasPart.Free || ld.HasPart.Sel != ".paywall" {
		t.Errorf("bad markup: %s", m[1])
	}
	// Google only accepts class selectors: the element must exist.
	if !strings.Contains(body, `class="paywall"`) {
		t.Error("no .paywall element")
	}
}

func TestWorkerAsset(t *testing.T) {
	f := newFixture(t)
	f.gate.opt.Next = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("worker request reached the origin") })
	f.gate.live.Store(&Settings{PriceSats: 10, Protect: []string{"/**"}, PoW: f.gate.opt.PoW, ChallengesPerMinute: 100})
	for _, target := range []string{WorkerPath, workerSrc} {
		rec := f.get(target, "", "")
		h := rec.Header()
		if rec.Code != http.StatusOK || rec.Body.String() != powWorkerJS ||
			!strings.HasPrefix(h.Get("Content-Type"), "text/javascript") ||
			h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Cache-Control"), "max-age") ||
			h.Get("Content-Security-Policy") != "default-src 'none'" {
			t.Fatalf("%s: %d %v", target, rec.Code, h)
		}
	}
	req := httptest.NewRequest(http.MethodHead, WorkerPath, nil)
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	req = httptest.NewRequest(http.MethodPost, WorkerPath, strings.NewReader("x"))
	rec = httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestArgon2idHidesThePuzzle(t *testing.T) {
	f := newFixture(t)
	f.pow = pow.Params{Algorithm: pow.Argon2id, Difficulty: 4, MemoryMiB: 1}
	rec := f.get("/premium/article", "", "text/html")
	body := rec.Body.String()
	if rec.Code != http.StatusPaymentRequired || strings.Contains(body, `id="pow-box"`) || len(inlineScripts(body)) != 2 {
		t.Fatalf("Argon2id page offers a puzzle the browser cannot solve: %d", rec.Code)
	}
}

// Known vectors of the browser puzzle: the smallest nonce the JS worker
// finds from the given start, checked by the Go verifier.
func TestBrowserPoWKnownVectors(t *testing.T) {
	for _, v := range []struct {
		challenge string
		bits      int
		nonce     uint64
	}{
		{"abc", 12, 747},
		{strings.Repeat("x", 55), 14, 2828}, // padding crosses a block boundary
		{strings.Repeat("y", 70), 13, 1003501},
		{strings.Repeat("z", 120) + "é", 16, 298690},
	} {
		p := pow.Params{Algorithm: pow.SHA256d, Difficulty: v.bits}
		if !p.Check(v.challenge, v.nonce) {
			t.Errorf("%.12q… bits=%d: Go rejects nonce %d", v.challenge, v.bits, v.nonce)
		}
		if v.nonce > 0 && p.Check(v.challenge, v.nonce-1) {
			t.Errorf("%.12q…: nonce %d is not the first", v.challenge, v.nonce)
		}
	}
}

// The real worker, run by Node, solves a real challenge from the gate and
// the gate redeems it the way the page does: Authorization + redeem header.
func TestBrowserWorkerEndToEnd(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	f := newFixture(t)
	f.pow.Difficulty = 14
	b := f.challenge(t, "/premium/article?x=1")

	dir := t.TempDir()
	worker := dir + "/worker.js"
	if err := os.WriteFile(worker, []byte(powWorkerJS), 0o600); err != nil {
		t.Fatal(err)
	}
	const run = `const vm=require("vm"),fs=require("fs");let out;const self={postMessage:m=>{out=m}};
vm.runInNewContext(fs.readFileSync(process.argv[1],"utf8"),{self,TextEncoder,Date,Int32Array,Uint8Array,String});
self.onmessage({data:{challenge:process.argv[2],difficulty:+process.argv[3],start:+process.argv[4]}});
console.log(JSON.stringify(out))`
	for _, start := range []string{"0", "1099511627776"} { // worker 0 and worker 1
		raw, err := exec.Command(node, "-e", run, worker, b.PoW.Challenge, "14", start).Output()
		if err != nil {
			t.Fatal(err)
		}
		var res struct {
			Attempts uint64
			Nonce    uint64
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if !(pow.Params{Algorithm: pow.SHA256d, Difficulty: 14}).Check(b.PoW.Challenge, res.Nonce) {
			t.Fatalf("start %s: Go rejects the JS nonce %d", start, res.Nonce)
		}
		req := httptest.NewRequest(http.MethodGet, "/premium/article?x=1", nil)
		req.Header.Set("Authorization", "PoW "+b.PoW.Challenge+":"+strconv.FormatUint(res.Nonce, 10))
		req.Header.Set(RedeemHeader, "cookie")
		rec := httptest.NewRecorder()
		f.gate.ServeHTTP(rec, req)
		if start == "0" {
			if rec.Code != http.StatusNoContent || !strings.Contains(strings.Join(rec.Header().Values("Set-Cookie"), ";"), "HttpOnly") {
				t.Fatalf("redeem: %d %v", rec.Code, rec.Header())
			}
		} else if rec.Code != http.StatusPaymentRequired { // same token: already spent
			t.Fatalf("a token must be spendable once, got %d", rec.Code)
		}
	}
}

// The mark in the 402 page (and the favicon) is the same file as the
// repo-root copy used in the README and on the demo site.
func TestLogoAssetMatchesEmbed(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", "assets", "logo.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != logoSVG {
		t.Fatal("assets/logo.svg and internal/gate/assets/logo.svg have drifted")
	}
	if !strings.Contains(logoSVG, `viewBox="0 0 128 128"`) || !strings.Contains(logoSVG, "#F7931A") {
		t.Fatal("logo.svg does not look like the Spikes402 mark")
	}
	if !strings.Contains(string(logoInline), `class="logo"`) || !strings.Contains(string(logoInline), `aria-hidden="true"`) {
		t.Fatal("inline logo is not marked decorative")
	}
	if !strings.HasPrefix(string(logoFavicon), "data:image/svg+xml;base64,") {
		t.Fatalf("favicon is not a data URI: %s", logoFavicon[:40])
	}
}

func TestWallPriceUnit(t *testing.T) {
	if satUnit(1) != "sat" || satUnit(2) != "sats" || satUnit(10) != "sats" {
		t.Fatalf("satUnit: 1=%q 2=%q 10=%q", satUnit(1), satUnit(2), satUnit(10))
	}

	f := newFixture(t)
	body := f.get("/premium/article", "", "text/html").Body.String()
	if !strings.Contains(body, "This page costs <strong>10 sats</strong>") {
		t.Error("default price should read \"10 sats\"")
	}

	f.gate.live.Store(&Settings{PriceSats: 1, Protect: []string{"/premium/**"}, PoW: f.gate.opt.PoW, ChallengesPerMinute: 1000})
	body = f.get("/premium/article", "", "text/html").Body.String()
	if !strings.Contains(body, "This page costs <strong>1 sat</strong>") {
		t.Error("a price of 1 should read \"1 sat\"")
	}
	if strings.Contains(body, "1 sats") {
		t.Error("a price of 1 still says \"1 sats\"")
	}
}
