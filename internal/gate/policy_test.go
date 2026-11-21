package gate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/botid"
	"github.com/Choffolo/spikes402/pow"
	"github.com/Choffolo/spikes402/webbotauth"
)

const gptUA = "Mozilla/5.0; compatible; GPTBot/1.2; +https://openai.com/gptbot"

// withBots adds a classifier that knows GPTBot comes from 20.0.0.0/24.
func withBots(f *fixture, sig *webbotauth.Verifier) {
	c := botid.New([]botid.Bot{{Name: "GPTBot", UserAgent: "GPTBot", IPRangesURL: "x"}}, sig)
	c.SetRanges("GPTBot", []netip.Prefix{netip.MustParsePrefix("20.0.0.0/24")})
	c.TrustedAgents = []string{"127.0.0.1"} // the test directory servers
	f.gate.opt.Classifier = c
}

// do sends a request from ip with user agent ua.
func (f *fixture) do(target, ip, ua, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = net.JoinHostPort(ip, "1234")
	req.Header.Set("User-Agent", ua)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) setPolicies(p map[string]Policy) {
	s := f.gate.Settings()
	s.Policies = p
	if err := f.gate.UpdateSettings(s); err != nil {
		panic(err)
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) challengeBody {
	t.Helper()
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d %s, want 402", rec.Code, rec.Body)
	}
	var b challengeBody
	if err := json.NewDecoder(rec.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDefaultEveryonePaysTheSame(t *testing.T) {
	f := newFixture(t)
	withBots(f, nil)
	for _, tc := range []struct{ ip, ua, class string }{
		{"20.0.0.5", gptUA, `verified; name="GPTBot"; method=ip-list`},
		{"6.6.6.6", gptUA, `impostor; name="GPTBot"; method=ip-list`},
		{"7.7.7.7", "curl/8", "unknown"},
	} {
		rec := f.do("/premium/a", tc.ip, tc.ua, "")
		if got := rec.Header().Get(ClassHeader); got != tc.class {
			t.Errorf("%s: class %q, want %q", tc.ip, got, tc.class)
		}
		b := decode(t, rec)
		if b.PriceSats != 10 || b.L402 == nil || b.PoW.Difficulty != testDifficulty {
			t.Errorf("%s: got price %d, l402 %v, difficulty %d", tc.ip, b.PriceSats, b.L402 != nil, b.PoW.Difficulty)
		}
	}
	// The header is on paid responses too, and not on unprotected paths.
	b := decode(t, f.do("/premium/b", "20.0.0.5", gptUA, ""))
	rec := f.do("/premium/b", "20.0.0.5", gptUA, solve(t, b))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get(ClassHeader), "verified") {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get(ClassHeader))
	}
	if rec := f.do("/free", "20.0.0.5", gptUA, ""); rec.Header().Get(ClassHeader) != "" {
		t.Fatal("class header on an unprotected path")
	}
}

func TestPolicies(t *testing.T) {
	f := newFixture(t)
	withBots(f, nil)
	f.setPolicies(map[string]Policy{
		"GPTBot":   {Action: ActionFree},
		"impostor": {Action: ActionPoWOnly, PriceMultiplier: 3},
		"unknown":  {Action: ActionPay, PriceMultiplier: 0.01}, // rounds up to 1 sat
	})
	if rec := f.do("/premium/a", "20.0.0.5", gptUA, ""); rec.Code != http.StatusOK {
		t.Fatalf("verified GPTBot with free policy: got %d", rec.Code)
	}
	// An impostor never gets the named bot's policy.
	b := decode(t, f.do("/premium/a", "6.6.6.6", gptUA, ""))
	if b.L402 != nil || b.PriceSats != 30 {
		t.Fatalf("impostor: got l402 %v, price %d", b.L402 != nil, b.PriceSats)
	}
	if rec := f.do("/premium/a", "6.6.6.6", gptUA, solve(t, b)); rec.Code != http.StatusOK {
		t.Fatalf("impostor solving PoW: got %d", rec.Code)
	}
	if b := decode(t, f.do("/premium/a", "7.7.7.7", "curl", "")); b.PriceSats != 1 || b.L402 == nil {
		t.Fatalf("unknown: got %+v", b)
	}

	f.setPolicies(map[string]Policy{"impostor": {Action: ActionBlock}})
	if rec := f.do("/premium/a", "6.6.6.6", gptUA, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("block: got %d", rec.Code)
	}
}

func TestPriceDependsOnPolicy(t *testing.T) {
	f := newFixture(t)
	withBots(f, nil)
	var seen []int64
	set := f.gate.Settings()
	set.PoW = func(price int64) pow.Params {
		seen = append(seen, price)
		return f.pow
	}
	f.gate.UpdateSettings(set)
	f.setPolicies(map[string]Policy{"verified": {Action: ActionPay, PriceMultiplier: 2}})
	b := decode(t, f.do("/premium/a", "20.0.0.5", gptUA, ""))
	if b.PriceSats != 20 || len(seen) != 1 || seen[0] != 20 {
		t.Fatalf("price %d, puzzle priced at %v", b.PriceSats, seen)
	}
	if !strings.HasPrefix(b.L402.Invoice, "lnfake20sat1") {
		t.Fatalf("invoice %s", b.L402.Invoice)
	}
}

func TestPolicyValidate(t *testing.T) {
	for _, p := range []Policy{{Action: "maybe"}, {Action: ActionPay, PriceMultiplier: -1}, {Action: ActionPay, PriceMultiplier: 1e9}} {
		if p.Validate() == nil {
			t.Errorf("%+v accepted", p)
		}
	}
	if (Policy{Action: ActionPay, PriceMultiplier: 1e3}).price(1e12) != 1e12 {
		t.Fatal("price not capped")
	}
}

func TestSignedBotThroughGate(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	dir := httptest.NewServer(webbotauth.DirectoryHandler(key))
	defer dir.Close()
	f := newFixture(t)
	withBots(f, webbotauth.NewVerifier(webbotauth.NewDirectories(true)))
	f.setPolicies(map[string]Policy{"verified-signed": {Action: ActionPoWOnly}})

	signed := func(target string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, target, nil) // https:// sets req.TLS
		req.RemoteAddr = "6.6.6.6:1234"
		(&webbotauth.Signer{Key: key, AgentURL: dir.URL + webbotauth.DirectoryPath}).Sign(req)
		return req
	}
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, signed("https://site.test/premium/a"))
	want := `verified-signed; name="` + dir.URL + webbotauth.DirectoryPath + `"; method=web-bot-auth`
	if got := rec.Header().Get(ClassHeader); got != want {
		t.Fatalf("class %q, want %q", got, want)
	}
	if b := decode(t, rec); b.L402 != nil {
		t.Fatal("pow-only policy ignored")
	}
	// A valid signature whose directory is not on a trusted domain gets no
	// "verified-signed" treatment: anyone can publish a key.
	f.gate.opt.Classifier.TrustedAgents = []string{"bots.example"}
	rec = httptest.NewRecorder()
	f.gate.ServeHTTP(rec, signed("https://site.test/premium/b"))
	if got := rec.Header().Get(ClassHeader); !strings.HasPrefix(got, "unknown") || !strings.Contains(got, "not on a trusted domain") {
		t.Fatalf("untrusted agent: got %q", got)
	}
	if b := decode(t, rec); b.L402 == nil {
		t.Fatal("untrusted agent must pay like everyone")
	}
	// Over plain http the signature is not trusted.
	f.gate.opt.Classifier = nil
	withBots(f, webbotauth.NewVerifier(webbotauth.NewDirectories(false)))
	rec = httptest.NewRecorder()
	f.gate.ServeHTTP(rec, signed("http://site.test/premium/a"))
	if got := rec.Header().Get(ClassHeader); !strings.HasPrefix(got, "unknown") {
		t.Fatalf("got %q", got)
	}
}

func TestScheme(t *testing.T) {
	f := newFixture(t)
	_, proxy, _ := net.ParseCIDR("10.0.0.0/8")
	f.gate.opt.TrustedProxies = []*net.IPNet{proxy}
	for _, tc := range []struct{ target, remote, proto, want string }{
		{"https://x/", "6.6.6.6:1", "", "https"},
		{"http://x/", "6.6.6.6:1", "https", "http"}, // untrusted header ignored
		{"http://x/", "10.0.0.1:1", "https", "https"},
		{"http://x/", "10.0.0.1:1", "HTTPS, http", "https"},
		{"http://x/", "10.0.0.1:1", "http", "http"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		req.RemoteAddr = tc.remote
		if tc.proto != "" {
			req.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		if got := f.gate.scheme(req); got != tc.want {
			t.Errorf("%+v: got %s", tc, got)
		}
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitize("a\nb\r\x00c"); got != "a?b??c" {
		t.Fatalf("got %q", got)
	}
	if got := sanitize(strings.Repeat("x", 500)); len(got) != 203 {
		t.Fatalf("len %d", len(got))
	}
}

// --- Argon2id ---

func argonFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.pow = pow.Params{Algorithm: pow.Argon2id, Difficulty: 1, MemoryMiB: 8}
	return f
}

func wrongNonce(b challengeBody) string {
	var n uint64
	for params(b).Check(b.PoW.Challenge, n) {
		n++
	}
	return "PoW " + b.PoW.Challenge + ":" + strconv.FormatUint(n, 10)
}

func TestArgonSolveAndRedeem(t *testing.T) {
	f := argonFixture(t)
	rec := f.get("/premium/a", "", "")
	if h := rec.Header().Values("WWW-Authenticate")[1]; !strings.Contains(h, `algorithm="argon2id", memory-mib="8"`) {
		t.Fatalf("header %q", h)
	}
	b := decode(t, rec)
	if b.PoW.Algorithm != pow.Argon2id || b.PoW.MemoryMiB != 8 {
		t.Fatalf("got %+v", b.PoW)
	}
	if rec := f.get("/premium/a", solve(t, b), ""); rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestArgonWrongAnswerBurnsToken(t *testing.T) {
	f := argonFixture(t)
	b := f.challenge(t, "/premium/a")
	if rec := f.get("/premium/a", wrongNonce(b), ""); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("got %d", rec.Code)
	}
	// Even the right answer is refused now: one check per token.
	rec := f.get("/premium/a", solve(t, b), "")
	if got := decode(t, rec); got.Problem != errReplay.Error() {
		t.Fatalf("problem %q", got.Problem)
	}
}

func TestArgonVerificationGuards(t *testing.T) {
	f := argonFixture(t)
	b := f.challenge(t, "/premium/a")
	good := solve(t, b)
	if rec := f.get("/premium/a", good, ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}

	// Fill the Argon2id memory budget so a new check cannot start.
	if !f.gate.argon.acquire(f.gate.argon.max, 0) {
		t.Fatal("hold argon budget")
	}
	// Cheap rejections still work and never reach Argon2id: a used token,
	// a forged token, an expired one.
	if got := decode(t, f.get("/premium/a", good, "")); got.Problem != errReplay.Error() {
		t.Fatalf("replay: %q", got.Problem)
	}
	forged := strings.Replace(good, "PoW ", "PoW x", 1)
	if got := decode(t, f.get("/premium/a", forged, "")); got.Problem != errBadToken.Error() {
		t.Fatalf("forged: %q", got.Problem)
	}
	// A genuine new token has to wait: 503 with Retry-After, token kept.
	b2 := f.challenge(t, "/premium/b")
	good2 := solve(t, b2)
	rec := f.get("/premium/b", good2, "")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	f.gate.argon.release(f.gate.argon.max)
	if rec := f.get("/premium/b", good2, ""); rec.Code != http.StatusOK {
		t.Fatalf("after the budget was freed: got %d", rec.Code)
	}
}

func TestArgonVerificationRateLimited(t *testing.T) {
	f := argonFixture(t)
	f.gate.limiter = newRateLimiter(3)
	f.gate.opt.ChallengeTTL = 2 * time.Minute
	b := f.challenge(t, "/premium/a") // uses 1 of 3
	good := solve(t, b)
	f.challenge(t, "/premium/b")
	f.challenge(t, "/premium/c") // 3 of 3
	rec := f.get("/premium/a", good, "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("got %d", rec.Code)
	}
	f.now = f.now.Add(61e9)
	if rec := f.get("/premium/a", good, ""); rec.Code != http.StatusOK {
		t.Fatalf("next minute: got %d", rec.Code)
	}
}

func TestArgonClaimsAreSigned(t *testing.T) {
	// A client cannot lower the memory or switch algorithm: the parameters
	// live inside the signed token.
	f := argonFixture(t)
	b := f.challenge(t, "/premium/a")
	c, err := f.gate.sign.verify(b.PoW.Challenge)
	if err != nil || c.PowAlg != pow.Argon2id || c.PowMem != 8 {
		t.Fatalf("%+v %v", c, err)
	}
	c.PowAlg = ""
	if _, err := f.gate.sign.verify(strings.Replace(b.PoW.Challenge, ".", "x.", 1)); err == nil {
		t.Fatal("tampered token accepted")
	}
}
