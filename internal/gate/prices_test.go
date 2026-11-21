package gate

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/pow"
)

func sectionFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	opt := f.gate.opt
	opt.Protect = []string{"/articles/**", "/premium/**", "/*.pdf"}
	opt.PriceSats = 10
	opt.Prices = []PriceRule{
		{Pattern: "/premium/special/**", PriceSats: 1},
		{Pattern: "/premium/**", PriceSats: 20},
		{Pattern: "/articles/**", PriceSats: 5},
	}
	f.gate = New(opt)
	f.gate.now = func() time.Time { return f.now }
	return f
}

func TestSectionPrices(t *testing.T) {
	f := sectionFixture(t)
	cases := map[string]int64{
		"/articles/hello":    5,
		"/ARTICLES/hello":    5, // same canonical path as protect
		"/articles/hello;x":  5,
		"/premium/a":         20,
		"/premium/special/a": 1, // first matching rule wins
		"/premium/special":   1,
		"/report.pdf":        10, // protected, no rule: default
	}
	for path, want := range cases {
		rec := f.rawGet(t, path)
		if rec.Code != http.StatusPaymentRequired {
			t.Errorf("%s: got %d, want 402", path, rec.Code)
			continue
		}
		var b challengeBody
		if err := json.NewDecoder(rec.Body).Decode(&b); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if b.PriceSats != want {
			t.Errorf("%s: price %d, want %d", path, b.PriceSats, want)
		}
		if b.L402 == nil || !strings.Contains(b.L402.Invoice, "lnfake"+strconv.FormatInt(want, 10)+"sat") {
			t.Errorf("%s: invoice %v does not use %d sats", path, b.L402, want)
		}
		c, err := f.gate.sign.verify(b.L402.Token)
		if err != nil || c.PriceSats != want {
			t.Errorf("%s: token price %d (%v), want %d", path, c.PriceSats, err, want)
		}
	}
}

func TestCheapTokenCannotUnlockExpensivePath(t *testing.T) {
	f := sectionFixture(t)
	cheap := f.challenge(t, "/articles/hello")
	if cheap.PriceSats != 5 {
		t.Fatalf("cheap price %d", cheap.PriceSats)
	}
	auth := f.pay(t, cheap)
	if rec := f.get("/articles/hello", auth, ""); rec.Code != http.StatusOK {
		t.Fatalf("paying for the cheap page should work: %d %q", rec.Code, rec.Body.String())
	}

	// A token paid for a cheap page cannot be reused on a dearer one.
	auth = f.pay(t, f.challenge(t, "/articles/other"))
	rec := f.get("/premium/secret", auth, "")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), errWrongTarget.Error()) {
		t.Fatalf("cross-path: got %d %q", rec.Code, rec.Body.String())
	}

	// Same resource as the expensive path, but a token bound to 5 sats.
	// The resource hash alone would not catch this; the bound price must.
	expensive := f.challenge(t, "/premium/secret")
	low := mustDecodeClaims(t, expensive.L402.Token)
	low.PriceSats = 5
	tok, err := f.gate.sign.sign(low)
	if err != nil {
		t.Fatal(err)
	}
	rec = f.get("/premium/secret", "PoW "+tok+":0", "")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), errWrongPrice.Error()) {
		t.Fatalf("underpriced token: got %d %q", rec.Code, rec.Body.String())
	}

	auth = f.pay(t, f.challenge(t, "/premium/secret"))
	if rec := f.get("/premium/secret", auth, ""); rec.Code != http.StatusOK {
		t.Fatalf("full price: got %d %q", rec.Code, rec.Body.String())
	}
}

// A price rise must not void what people already hold: a paid invoice is
// honoured at the price it was issued for, a proof of work is not (its
// difficulty was set for the old price).
func TestPriceRiseHonoursPaidInvoiceButNotOldPoW(t *testing.T) {
	f := newFixture(t)
	paid := f.pay(t, f.challenge(t, "/premium/a"))
	work := solve(t, f.challenge(t, "/premium/b"))
	s := f.gate.Settings()
	s.PriceSats = 50
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if rec := f.get("/premium/a", paid, ""); rec.Code != http.StatusOK {
		t.Fatalf("paid invoice after price rise: got %d %q", rec.Code, rec.Body.String())
	}
	rec := f.get("/premium/b", work, "")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), errWrongPrice.Error()) {
		t.Fatalf("old proof of work after price rise: got %d %q", rec.Code, rec.Body.String())
	}
}

// Tokens from before the price was signed into them (claim missing, read as 0):
// a payment is honoured (the invoice was paid), work is not (price unknown).
func TestLegacyTokenWithoutPrice(t *testing.T) {
	f := newFixture(t)
	legacy := func(path string) challengeBody {
		b := f.challenge(t, path)
		c := mustDecodeClaims(t, b.L402.Token)
		c.PriceSats = 0
		tok, err := f.gate.sign.sign(c)
		if err != nil {
			t.Fatal(err)
		}
		b.L402.Token, b.PoW.Challenge = tok, tok
		return b
	}
	if rec := f.get("/premium/a", f.pay(t, legacy("/premium/a")), ""); rec.Code != http.StatusOK {
		t.Fatalf("legacy paid token: got %d", rec.Code)
	}
	if rec := f.get("/premium/b", solve(t, legacy("/premium/b")), ""); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("legacy PoW token: got %d", rec.Code)
	}
}

func TestSectionPoWTracksPrice(t *testing.T) {
	f := newFixture(t)
	var seen []int64
	opt := f.gate.opt
	opt.Protect = []string{"/articles/**", "/premium/**"}
	opt.PriceSats = 10
	opt.Prices = []PriceRule{{Pattern: "/articles/**", PriceSats: 5}, {Pattern: "/premium/**", PriceSats: 20}}
	opt.PoW = func(price int64) pow.Params {
		seen = append(seen, price)
		return pow.ForPrice(pow.SHA256d, 0, price, 10)
	}
	f.gate = New(opt)
	f.gate.now = func() time.Time { return f.now }

	cheap := f.challenge(t, "/articles/a")
	dear := f.challenge(t, "/premium/a")
	if cheap.PoW.Difficulty >= dear.PoW.Difficulty {
		t.Fatalf("derived PoW should be harder for the dearer page: cheap=%d dear=%d", cheap.PoW.Difficulty, dear.PoW.Difficulty)
	}
	if len(seen) < 2 || seen[0] != 5 || seen[1] != 20 {
		t.Fatalf("PoW function prices: %v", seen)
	}
}

func TestUpdateSettingsChangesPathPrices(t *testing.T) {
	f := sectionFixture(t)
	if b := f.challenge(t, "/premium/a"); b.PriceSats != 20 {
		t.Fatalf("before: %d", b.PriceSats)
	}
	auth := f.pay(t, f.challenge(t, "/articles/hello"))
	s := f.gate.Settings()
	s.Prices = []PriceRule{{Pattern: "/articles/**", PriceSats: 50}}
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if b := f.challenge(t, "/articles/new"); b.PriceSats != 50 {
		t.Fatalf("live article price %d, want 50", b.PriceSats)
	}
	if b := f.challenge(t, "/premium/a"); b.PriceSats != 10 {
		t.Fatalf("removed rule should fall back to default: %d", b.PriceSats)
	}
	if rec := f.get("/articles/hello", auth, ""); rec.Code != http.StatusOK {
		t.Fatalf("paid cheap token after rise is still honoured: %d %q", rec.Code, rec.Body.String())
	}
}

func mustDecodeClaims(t *testing.T, token string) claims {
	t.Helper()
	body, _, ok := strings.Cut(token, ".")
	if !ok {
		t.Fatal("token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatal(err)
	}
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPauseServesProtectedPathsFree(t *testing.T) {
	f := newFixture(t)
	if rec := f.get("/premium/a", "", ""); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("before pause: %d", rec.Code)
	}
	s := f.gate.Settings()
	s.Paused = true
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	rec := f.get("/premium/a", "", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "content of /premium/a" {
		t.Fatalf("paused: %d %q", rec.Code, rec.Body.String())
	}
	if rec := f.get("/free", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("unprotected: %d", rec.Code)
	}
	s = f.gate.Settings()
	s.Paused = false
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if rec := f.get("/premium/a", "", ""); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("after unpause: %d", rec.Code)
	}
}

func TestProtectLiveUpdate(t *testing.T) {
	f := newFixture(t)
	if rec := f.get("/articles/a", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("unprotected: %d", rec.Code)
	}
	s := f.gate.Settings()
	s.Protect = []string{"/articles/**"}
	s.Prices = []PriceRule{{Pattern: "/articles/**", PriceSats: 7}}
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	rec := f.get("/articles/a", "", "")
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("newly protected: %d", rec.Code)
	}
	var b challengeBody
	if err := json.NewDecoder(rec.Body).Decode(&b); err != nil || b.PriceSats != 7 {
		t.Fatalf("price %v err=%v", b.PriceSats, err)
	}
	if rec := f.get("/premium/a", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("no longer protected: %d", rec.Code)
	}
}

func TestOldTokenStillBoundAfterProtectChange(t *testing.T) {
	f := sectionFixture(t)
	auth := f.pay(t, f.challenge(t, "/articles/hello"))
	if rec := f.get("/articles/hello", auth, ""); rec.Code != http.StatusOK {
		t.Fatalf("paid token: %d", rec.Code)
	}
	unused := f.pay(t, f.challenge(t, "/articles/hello"))

	s := f.gate.Settings()
	s.Protect = []string{"/articles/**", "/premium/**", "/new/**"}
	s.Prices = []PriceRule{
		{Pattern: "/articles/**", PriceSats: 50},
		{Pattern: "/premium/**", PriceSats: 20},
	}
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if rec := f.get("/premium/secret", unused, ""); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("cross-path after protect change: %d", rec.Code)
	}
	if rec := f.get("/articles/hello", unused, ""); rec.Code != http.StatusOK {
		t.Fatalf("paid token issued at 5 sats, price now 50: %d %q", rec.Code, rec.Body.String())
	}
	if rec := f.get("/articles/hello", auth, ""); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("a spent token must stay spent: %d", rec.Code)
	}
}
