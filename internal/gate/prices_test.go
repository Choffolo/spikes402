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

func TestDefaultPriceRiseRejectsOldToken(t *testing.T) {
	f := newFixture(t)
	auth := f.pay(t, f.challenge(t, "/premium/a"))
	s := f.gate.Settings()
	s.PriceSats = 50
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	rec := f.get("/premium/a", auth, "")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), errWrongPrice.Error()) {
		t.Fatalf("after price rise: got %d %q", rec.Code, rec.Body.String())
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
	rec := f.get("/articles/hello", auth, "")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), errWrongPrice.Error()) {
		t.Fatalf("old cheap token after rise: %d %q", rec.Code, rec.Body.String())
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
