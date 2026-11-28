package gate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
)

func newPayFixture(t *testing.T) *loginFixture {
	t.Helper()
	f := newLoginFixture(t)
	f.gate.opt.LoginPrice = &LoginPrice{BaseSats: 10}
	return f
}

func (f *loginFixture) invoiceFor(user, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, LoginInvoicePath, strings.NewReader(`{"method":"POST","path":"/login","username":"`+user+`"}`))
	r.RemoteAddr = ip + ":1"
	r.Header.Set("Content-Type", "application/json")
	return f.do(r)
}

func decodeInvoice(t *testing.T, rec *httptest.ResponseRecorder) loginInvoice {
	t.Helper()
	var inv loginInvoice
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&inv) != nil || inv.Invoice == "" || inv.Challenge == "" {
		t.Fatalf("no invoice in %d answer: %s", rec.Code, rec.Body)
	}
	return inv
}

func (f *loginFixture) pay(t *testing.T, inv loginInvoice) string {
	t.Helper()
	pre, err := f.gate.opt.Lightning.(*lightning.Fake).Pay(inv.Invoice)
	if err != nil {
		t.Fatal(err)
	}
	return pre
}

func (f *loginFixture) redeemPaid(auth, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, LoginRedeemPath, nil)
	r.RemoteAddr = ip + ":1"
	r.Header.Set("Authorization", auth)
	return f.do(r)
}

// A payment buys one attempt, nothing more: the origin still decides, a
// failure still escalates (and the price with it), and the pass is the
// same one-use, bound login pass a proof buys.
func TestLoginPayFlow(t *testing.T) {
	f := newPayFixture(t)
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	if ch.PriceSats != 10 || ch.PayInstead != LoginInvoicePath {
		t.Fatalf("402 should offer paying: %+v", ch)
	}
	inv := decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	if inv.PriceSats != 10 || inv.Level != LoginBaseLevel || !strings.HasPrefix(inv.QR, "data:image/png;base64,") {
		t.Fatalf("invoice %+v", inv)
	}
	if rec := f.redeemPaid("L402 "+inv.Challenge, "192.0.2.1"); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "unpaid") {
		t.Fatalf("unpaid: %d %s", rec.Code, rec.Body)
	}
	f.pay(t, inv)
	f.now = f.now.Add(2 * time.Second) // past the cached "unpaid"
	rec := f.redeemPaid("L402 "+inv.Challenge, "192.0.2.1")
	if rec.Code != http.StatusNoContent || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("paid: %d %s", rec.Code, rec.Body)
	}
	pass := rec.Result().Cookies()[0]
	if rec := f.redeemPaid("L402 "+inv.Challenge, "192.0.2.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("second redeem: %d", rec.Code)
	}
	// The origin refuses the login: still a failure, at full price.
	if rec := f.do(loginReq("alice", "192.0.2.1", "", pass)); rec.Code != http.StatusFound || f.calls != 1 {
		t.Fatalf("login with paid pass: %d", rec.Code)
	}
	if rec := f.do(loginReq("alice", "192.0.2.1", "", pass)); rec.Code != http.StatusPaymentRequired || f.calls != 1 {
		t.Fatalf("paid pass reused: %d", rec.Code)
	}
	inv2 := decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	if inv2.Level != LoginBaseLevel+1 || inv2.PriceSats != 20 {
		t.Fatalf("after a paid failure: level %d, %d sats; want %d, 20", inv2.Level, inv2.PriceSats, LoginBaseLevel+1)
	}
	// The preimage proves payment without asking the node; a wrong one does not.
	pre := f.pay(t, inv2)
	if rec := f.redeemPaid("L402 "+inv2.Challenge+":"+strings.Repeat("00", 32), "192.0.2.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong preimage: %d", rec.Code)
	}
	if rec := f.redeemPaid("L402 "+inv2.Challenge+":"+pre, "192.0.2.1"); rec.Code != http.StatusNoContent {
		t.Fatalf("preimage: %d %s", rec.Code, rec.Body)
	}
	// The origin accepts the next paid attempt: a login, as with work.
	f.succeed = func(string) bool { return true }
	inv3 := decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	f.pay(t, inv3)
	f.now = f.now.Add(2 * time.Second)
	rec = f.redeemPaid("L402 "+inv3.Challenge, "192.0.2.1")
	if rec := f.do(loginReq("alice", "192.0.2.1", "", rec.Result().Cookies()...)); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/home" {
		t.Fatalf("login: %d", rec.Code)
	}
}

// A paid invoice is bound like a proof: account, network, route and kind.
func TestLoginPayBinding(t *testing.T) {
	f := newPayFixture(t)
	inv := decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	pre := f.pay(t, inv)
	if rec := f.redeemPaid("L402 "+inv.Challenge+":"+pre, "203.0.113.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("other network: %d", rec.Code)
	}
	for name, auth := range map[string]string{
		"as a proof of work": "PoW " + inv.Challenge + ":1:1",
		"bad scheme":         "Bearer " + inv.Challenge,
	} {
		if rec := f.redeemPaid(auth, "192.0.2.1"); rec.Code == http.StatusNoContent {
			t.Errorf("%s: got a pass", name)
		}
	}
	// Not a paywall token either.
	r := httptest.NewRequest(http.MethodGet, ClaimPath, nil)
	r.Header.Set("Authorization", "Bearer "+inv.Challenge)
	if rec := f.do(r); rec.Code != http.StatusUnauthorized {
		t.Fatalf("claim: %d", rec.Code)
	}
	// A login proof-of-work token is not a paid one.
	ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", "")))
	if rec := f.redeemPaid("L402 "+ch.Challenge+":"+pre, "192.0.2.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("PoW token as paid: %d", rec.Code)
	}
	rec := f.redeemPaid("L402 "+inv.Challenge+":"+pre, "192.0.2.9") // same /24
	if rec.Code != http.StatusNoContent {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(loginReq("bob", "192.0.2.1", "", rec.Result().Cookies()...)); rec.Code != http.StatusPaymentRequired || f.calls != 0 {
		t.Fatalf("pass for another account: %d", rec.Code)
	}
}

// Once the level rises (here by another failure from the same network),
// a payment made at the old price no longer buys an attempt, before or
// after it was redeemed.
func TestLoginPayStaleAfterPriceRise(t *testing.T) {
	f := newPayFixture(t)
	inv := decodeInvoice(t, f.invoiceFor("carol", "192.0.2.1"))
	inv2 := decodeInvoice(t, f.invoiceFor("carol", "192.0.2.1"))
	pre, pre2 := f.pay(t, inv), f.pay(t, inv2)
	rec := f.redeemPaid("L402 "+inv2.Challenge+":"+pre2, "192.0.2.1")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("redeem: %d", rec.Code)
	}
	f.attempt(t, "carol", "192.0.2.1") // fails: level 24, 20 sats
	stale := f.redeemPaid("L402 "+inv.Challenge+":"+pre, "192.0.2.1")
	var body struct {
		Error     string `json:"error"`
		PriceSats int64  `json:"price_sats"`
		Level     int    `json:"level"`
	}
	if stale.Code != http.StatusConflict || json.NewDecoder(stale.Body).Decode(&body) != nil ||
		!strings.Contains(body.Error, "a new invoice costs 20 sats") || body.PriceSats != 20 || body.Level != LoginBaseLevel+1 {
		t.Fatalf("stale payment: %d %+v", stale.Code, body)
	}
	// Still unspent and still refused: no stockpile, whenever it is tried.
	if rec := f.redeemPaid("L402 "+inv.Challenge+":"+pre, "192.0.2.1"); rec.Code != http.StatusConflict {
		t.Fatalf("stale payment again: %d", rec.Code)
	}
	if rec := f.do(loginReq("carol", "192.0.2.1", "", rec.Result().Cookies()...)); rec.Code != http.StatusPaymentRequired || f.calls != 1 {
		t.Fatalf("stale paid pass: %d, %d origin calls", rec.Code, f.calls)
	}
}

// Limits fail closed: no invoice, a proof-of-work challenge instead.
func TestLoginPayLimits(t *testing.T) {
	refused := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %d, want 503: %s", rec.Code, rec.Body)
		}
		if ch := decodeChallenge(t, rec); ch.PriceSats != 0 || ch.PayInstead != "" || !strings.Contains(ch.Error, "proof-of-work") {
			t.Fatalf("fallback %+v", ch)
		}
	}
	redeem := func(t *testing.T, f *loginFixture, inv loginInvoice, ip string) {
		t.Helper()
		if rec := f.redeemPaid("L402 "+inv.Challenge+":"+f.pay(t, inv), ip); rec.Code != http.StatusNoContent {
			t.Fatalf("redeem: %d", rec.Code)
		}
	}

	t.Run("account and network open and per minute", func(t *testing.T) {
		f := newPayFixture(t)
		a := decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
		decodeInvoice(t, f.invoiceFor("Alice", "192.0.2.50")) // same account, same /24
		refused(t, f.invoiceFor("alice", "192.0.2.1"))
		// A third party elsewhere fills its own limits for alice, never hers.
		for range 2 {
			decodeInvoice(t, f.invoiceFor("alice", "198.51.100.1"))
		}
		refused(t, f.invoiceFor("alice", "198.51.100.1"))
		redeem(t, f, a, "192.0.2.1") // paid: no longer open
		for range 4 {
			redeem(t, f, decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1")), "192.0.2.1")
		}
		refused(t, f.invoiceFor("alice", "192.0.2.1")) // 6 this minute from here
		decodeInvoice(t, f.invoiceFor("bob", "192.0.2.1"))
		decodeInvoice(t, f.invoiceFor("alice", "203.0.113.1"))
		f.now = f.now.Add(loginInvoiceTTL) // open ones expired, new minute
		decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
		decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	})
	t.Run("network per minute", func(t *testing.T) {
		f := newPayFixture(t)
		for i := range 20 {
			decodeInvoice(t, f.invoiceFor("user"+string(rune('a'+i)), "192.0.2.1"))
		}
		refused(t, f.invoiceFor("zed", "192.0.2.77"))
		decodeInvoice(t, f.invoiceFor("zed", "198.51.100.1"))
		f.now = f.now.Add(time.Minute)
		decodeInvoice(t, f.invoiceFor("zed", "192.0.2.1"))
	})
	t.Run("global per minute and open", func(t *testing.T) {
		f := newPayFixture(t)
		ip := func(i int) string { return "10." + string(rune('0'+i)) + ".0.1" }
		for i := range 60 {
			decodeInvoice(t, f.invoiceFor("u"+ip(i%10)+string(rune('a'+i/10)), ip(i%10)))
		}
		refused(t, f.invoiceFor("late", "198.51.100.1"))
		f.now = f.now.Add(time.Minute)
		decodeInvoice(t, f.invoiceFor("late", "198.51.100.1"))

		f = newPayFixture(t)
		f.gate.loginPay.lim.Open = 2
		a := decodeInvoice(t, f.invoiceFor("a", "192.0.2.1"))
		decodeInvoice(t, f.invoiceFor("b", "198.51.100.1"))
		refused(t, f.invoiceFor("c", "203.0.113.1"))
		redeem(t, f, a, "192.0.2.1")
		decodeInvoice(t, f.invoiceFor("c", "203.0.113.1"))
	})
	t.Run("node guard and paying off", func(t *testing.T) {
		f := newPayFixture(t)
		f.gate.node.openUntil = f.now.Add(time.Hour) // breaker open
		refused(t, f.invoiceFor("alice", "192.0.2.1"))
		f = newLoginFixture(t) // no login_price
		refused(t, f.invoiceFor("alice", "192.0.2.1"))
		if ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", ""))); ch.PriceSats != 0 || ch.PayInstead != "" {
			t.Fatalf("paying offered while off: %+v", ch)
		}
		f.gate.opt.LoginPrice = &LoginPrice{BaseSats: 10}
		// Paused from the dashboard at run time (the live settings).
		set := f.gate.Settings()
		set.Paused = true
		if err := f.gate.UpdateSettings(set); err != nil {
			t.Fatal(err)
		}
		refused(t, f.invoiceFor("alice", "192.0.2.1"))
		if ch := decodeChallenge(t, f.do(loginReq("alice", "192.0.2.1", ""))); ch.PriceSats != 0 || ch.PayInstead != "" {
			t.Fatalf("paying offered while paused: %+v", ch)
		}
		set.Paused = false
		f.gate.UpdateSettings(set)
		decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	})
}

// The price is a function of the level alone, so it shows no more than
// the level: nothing of an account's failures below loginAcctShow.
func TestLoginPriceShowsNoMoreThanLevel(t *testing.T) {
	f := newPayFixture(t)
	f.gate.loginPay.lim = LoginInvoiceLimits{100, 100, 100, 100, 100}
	now := f.now
	for i := range 6 {
		f.gate.login.admit(f.gate.loginAccount("victim"), "n"+string(rune('0'+i)), LoginMaxLevel, now)
		want := int64(10)
		if i+1 >= loginAcctShow {
			want = 10 << loginAcctCap
		}
		if got := decodeInvoice(t, f.invoiceFor("victim", "203.0.113."+string(rune('1'+i)))).PriceSats; got != want {
			t.Fatalf("after %d failures elsewhere: %d sats, want %d", i+1, got, want)
		}
	}
}

func TestLoginPriceMapping(t *testing.T) {
	g := &Gate{opt: Options{LoginPrice: &LoginPrice{BaseSats: 5, MaxSats: 100}}}
	g.live.Store(&Settings{})
	route := &LoginRoute{}
	for lvl, want := range map[int]int64{LoginBaseLevel: 5, LoginBaseLevel + 1: 10, LoginBaseLevel + 4: 80, LoginBaseLevel + 5: 100, LoginMaxLevel: 100, LoginMaxLevel + 50: 100} {
		if got := g.loginPrice(route, lvl); got != want {
			t.Errorf("level %d: %d, want %d", lvl, got, want)
		}
	}
	g.opt.LoginPrice.MaxSats = 0 // default cap: 64 × base_sats
	if got := g.loginPrice(route, LoginMaxLevel+50); got != 5*loginDefaultMaxFactor {
		t.Errorf("default cap: %d", got)
	}
	if got := g.loginPrice(route, LoginBaseLevel+5); got != 5<<5 {
		t.Errorf("below the default cap: %d", got)
	}
	g.opt.LoginPrice.BaseSats = maxLoginBaseSats
	if got := g.loginPrice(route, LoginMaxLevel); got != maxLoginMaxSats {
		t.Errorf("default cap is bounded: %d", got)
	}
	g.opt.LoginPrice.BaseSats = 5
	g.live.Store(&Settings{Paused: true}) // the live pause, not the startup option
	if got := g.loginPrice(route, LoginBaseLevel); got != 0 {
		t.Errorf("paused: %d", got)
	}
	g.live.Store(&Settings{})
	if got := g.loginPrice(&LoginRoute{Price: &LoginPrice{BaseSats: 1}}, LoginBaseLevel+2); got != 4 {
		t.Errorf("route override: %d", got)
	}
	if got := g.loginPrice(&LoginRoute{Price: &LoginPrice{}}, LoginBaseLevel); got != 0 {
		t.Errorf("route off: %d", got)
	}
	for _, bad := range []LoginPrice{{BaseSats: -1}, {BaseSats: maxLoginBaseSats + 1}, {BaseSats: 10, MaxSats: 5}, {BaseSats: 1, MaxSats: maxLoginMaxSats + 1}, {BaseSats: 1, MaxSats: -1}} {
		if CheckLoginPrice(&bad) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if l, err := (LoginInvoiceLimits{PerMinute: 7}).WithDefaults(); err != nil || l.PerMinute != 7 || l.Open != 500 || l.AccountNetworkOpen != 2 {
		t.Errorf("defaults: %+v %v", l, err)
	}
	if _, err := (LoginInvoiceLimits{Open: -1}).WithDefaults(); err == nil {
		t.Error("accepted a negative limit")
	}
	if CheckLoginRoutes([]LoginRoute{{Path: "/login", UsernameField: "u", Success: LoginSuccess{Status: []int{200}}, Price: &LoginPrice{BaseSats: -1}}}) == nil {
		t.Error("accepted a bad route price")
	}
}

// Every reason for not handing out an invoice gives the same answer, so
// a 503 does not tell which limit was reached (or that paying is off).
func TestLoginPay503RevealsNoReason(t *testing.T) {
	norm := func(t *testing.T, rec *httptest.ResponseRecorder) string {
		t.Helper()
		var m map[string]any
		if rec.Code != http.StatusServiceUnavailable || json.NewDecoder(rec.Body).Decode(&m) != nil {
			t.Fatalf("got %d", rec.Code)
		}
		delete(m, "challenge")
		delete(m, "expires_at")
		h := rec.Header().Clone()
		b, _ := json.Marshal(map[string]any{"body": m, "headers": h})
		return string(b)
	}
	var got []string
	f := newPayFixture(t)
	f.gate.loginPay.lim = LoginInvoiceLimits{1, 100, 100, 100, 100}
	decodeInvoice(t, f.invoiceFor("a", "192.0.2.1"))
	got = append(got, norm(t, f.invoiceFor("a", "192.0.2.1"))) // account and network open
	f.gate.loginPay.lim = LoginInvoiceLimits{100, 1, 100, 100, 100}
	decodeInvoice(t, f.invoiceFor("b", "192.0.2.1"))
	got = append(got, norm(t, f.invoiceFor("b", "192.0.2.1"))) // account and network per minute
	f.gate.loginPay.lim = LoginInvoiceLimits{100, 100, 1, 100, 100}
	decodeInvoice(t, f.invoiceFor("c", "198.51.100.1"))
	got = append(got, norm(t, f.invoiceFor("d", "198.51.100.1"))) // network per minute
	f.gate.loginPay.lim = LoginInvoiceLimits{100, 100, 100, 100, 1}
	f.now = f.now.Add(loginInvoiceTTL)
	decodeInvoice(t, f.invoiceFor("e", "203.0.113.1"))
	got = append(got, norm(t, f.invoiceFor("f", "10.0.0.1"))) // open overall
	f.gate.loginPay.lim = LoginInvoiceLimits{100, 100, 100, 1, 100}
	f.now = f.now.Add(loginInvoiceTTL)
	decodeInvoice(t, f.invoiceFor("g", "10.1.0.1"))
	got = append(got, norm(t, f.invoiceFor("h", "10.2.0.1"))) // per minute overall
	f.gate.loginPay.lim = DefaultLoginInvoiceLimits
	f.gate.node.openUntil = f.now.Add(time.Hour)
	got = append(got, norm(t, f.invoiceFor("i", "10.3.0.1"))) // node breaker
	f.gate.node.openUntil = time.Time{}
	f.gate.opt.LoginPrice = nil
	got = append(got, norm(t, f.invoiceFor("j", "10.4.0.1"))) // paying off
	for i, g := range got {
		if g != got[0] {
			t.Errorf("answer %d differs:\n%s\n%s", i, g, got[0])
		}
	}
}

// A hostile page must not create invoices (or challenges, or redeem) in a
// visitor's browser: simple cross-site POSTs, foreign Origins and
// cross-site or same-site fetches are refused.
func TestLoginPayCrossSite(t *testing.T) {
	f := newPayFixture(t)
	f.gate.loginHosts = []string{"example.com", "127.0.0.1:8080"}
	req := func(path, ct string, h map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"method":"POST","path":"/login","username":"alice"}`))
		r.Host = "proxy.internal:8402"
		r.RemoteAddr = "192.0.2.1:1"
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return f.do(r)
	}
	for name, c := range map[string]struct {
		ct   string
		h    map[string]string
		want int
	}{
		"text/plain":            {"text/plain", nil, http.StatusUnsupportedMediaType},
		"form":                  {"application/x-www-form-urlencoded", nil, http.StatusUnsupportedMediaType},
		"no Content-Type":       {"", nil, http.StatusUnsupportedMediaType},
		"cross-site fetch":      {"application/json", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		"same-site fetch":       {"application/json", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		"foreign Origin":        {"application/json", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		"null Origin":           {"application/json", map[string]string{"Origin": "null"}, http.StatusForbidden},
		"lookalike Origin":      {"application/json", map[string]string{"Origin": "https://example.com.evil.example"}, http.StatusForbidden},
		"API client":            {"application/json", nil, http.StatusOK},
		"same-origin fetch":     {"application/json; charset=utf-8", map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://proxy.internal:8402"}, http.StatusOK},
		"public host Origin":    {"application/json", map[string]string{"Origin": "https://example.com"}, http.StatusOK},
		"origin host as Origin": {"application/json", map[string]string{"Origin": "http://127.0.0.1:8080"}, http.StatusOK},
	} {
		f.gate.loginPay = newLoginInvoices(LoginInvoiceLimits{100, 100, 100, 100, 100})
		if rec := req(LoginInvoicePath, c.ct, c.h); rec.Code != c.want {
			t.Errorf("invoice, %s: got %d, want %d: %s", name, rec.Code, c.want, rec.Body)
		}
	}
	evil := map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site", "Authorization": "L402 x:y"}
	for _, p := range []string{LoginChallengePath, LoginRedeemPath} {
		if rec := req(p, "application/json", evil); rec.Code != http.StatusForbidden {
			t.Errorf("%s cross-site: got %d", p, rec.Code)
		}
	}
	if rec := req(LoginChallengePath, "text/plain", map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != http.StatusOK {
		t.Errorf("same-origin challenge: got %d", rec.Code) // the challenge's API stays as it was
	}
}

// underpayingNode reports every paid invoice as paid with one sat less.
type underpayingNode struct{ *lightning.Fake }

func (u underpayingNode) LookupIncoming(ctx context.Context, h [32]byte) (lightning.IncomingPayment, error) {
	p, err := u.Fake.LookupIncoming(ctx, h)
	p.ReceivedSat--
	return p, err
}

// sameHashNode hands out one payment hash for every invoice.
type sameHashNode struct {
	*lightning.Fake
	first *lightning.Invoice
}

func (s *sameHashNode) CreateInvoice(ctx context.Context, sat int64, memo string, exp time.Duration) (lightning.Invoice, error) {
	if s.first != nil {
		return *s.first, nil
	}
	inv, err := s.Fake.CreateInvoice(ctx, sat, memo, exp)
	s.first = &inv
	return inv, err
}

func TestLoginPayUnderpaidAndRepeatedHash(t *testing.T) {
	f := newPayFixture(t)
	fake := f.gate.opt.Lightning.(*lightning.Fake)
	f.gate.invoices.lookup = underpayingNode{fake}
	inv := decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	pre := f.pay(t, inv)
	if rec := f.redeemPaid("L402 "+inv.Challenge, "192.0.2.1"); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "less than the price") {
		t.Fatalf("underpaid lookup: %d %s", rec.Code, rec.Body)
	}
	f.gate.invoices.lookup = fake

	// One preimage, two tokens with the same payment hash: one attempt.
	f = newPayFixture(t)
	node := &sameHashNode{Fake: f.gate.opt.Lightning.(*lightning.Fake)}
	f.gate.node.backend = node
	a := decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	b := decodeInvoice(t, f.invoiceFor("alice", "192.0.2.1"))
	pre = f.pay(t, a)
	if rec := f.redeemPaid("L402 "+a.Challenge+":"+pre, "192.0.2.1"); rec.Code != http.StatusNoContent {
		t.Fatalf("first: %d", rec.Code)
	}
	if rec := f.redeemPaid("L402 "+b.Challenge+":"+pre, "192.0.2.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("same hash, second token: %d %s", rec.Code, rec.Body)
	}
	if rec := f.redeemPaid("L402 "+b.Challenge, "192.0.2.1"); rec.Code != http.StatusForbidden {
		t.Fatalf("same hash, second token by lookup: %d %s", rec.Code, rec.Body)
	}
}
