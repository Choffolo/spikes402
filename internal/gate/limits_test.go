package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
)

// countingNode counts the invoices created.
type countingNode struct {
	lightning.Backend
	n atomic.Int64
}

func (c *countingNode) CreateInvoice(ctx context.Context, amt int64, memo string, exp time.Duration) (lightning.Invoice, error) {
	c.n.Add(1)
	return c.Backend.CreateInvoice(ctx, amt, memo, exp)
}

func (f *fixture) countInvoices() *countingNode {
	c := &countingNode{Backend: f.ln}
	f.gate.node.backend = c
	return c
}

func (f *fixture) getFrom(remoteAddr, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	return rec
}

func TestRateLimitPerClient(t *testing.T) {
	f := newFixture(t)
	f.gate.limiter = newRateLimiter(3)
	for i := 0; i < 3; i++ {
		if rec := f.getFrom("198.51.100.1:1000", "/premium/a"); rec.Code != http.StatusPaymentRequired {
			t.Fatalf("request %d: got %d", i, rec.Code)
		}
	}
	rec := f.getFrom("198.51.100.1:2000", "/premium/b") // other port and page, same client
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over the limit: got %d", rec.Code)
	}
	if s, _ := strconv.Atoi(rec.Header().Get("Retry-After")); s < 1 || s > 60 {
		t.Fatalf("bad Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if rec := f.getFrom("198.51.100.2:1000", "/premium/a"); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("other client: got %d", rec.Code)
	}
	if rec := f.getFrom("198.51.100.1:1000", "/free"); rec.Code != http.StatusOK {
		t.Fatalf("free pages are never limited: got %d", rec.Code)
	}
	f.now = f.now.Add(time.Minute)
	if rec := f.getFrom("198.51.100.1:1000", "/premium/a"); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("next minute: got %d", rec.Code)
	}
}

func TestRateLimitIPv6Network(t *testing.T) {
	f := newFixture(t)
	f.gate.limiter = newRateLimiter(1)
	f.getFrom("[2001:db8:1:2::1]:1000", "/premium/a")
	if rec := f.getFrom("[2001:db8:1:2:ffff::9]:1000", "/premium/a"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("same /64 must share the limit: got %d", rec.Code)
	}
	if rec := f.getFrom("[2001:db8:1:3::1]:1000", "/premium/a"); rec.Code != http.StatusPaymentRequired {
		t.Fatalf("other /64: got %d", rec.Code)
	}
}

func TestRateLimiterMemoryBounded(t *testing.T) {
	l := newRateLimiter(5)
	now := time.Unix(0, 0)
	for i := 0; i < maxTrackedClients+50; i++ {
		l.allow("client"+strconv.Itoa(i), now)
	}
	if n := len(l.clients); n > maxTrackedClients+1 {
		t.Fatalf("tracked %d clients", n)
	}
	// Newcomers beyond the cap share one allowance.
	if ok, _ := l.allow("one-more", now); ok {
		t.Fatal("overflow allowance should be used up")
	}
	l.allow("late", now.Add(time.Minute))
	if n := len(l.clients); n != 1 {
		t.Fatalf("expired windows not evicted: %d left", n)
	}
}

// offerFor asks for target from remoteAddr with an optional session
// cookie and returns the offer and the session cookie set, if any.
func (f *fixture) offerFor(t *testing.T, remoteAddr, target, session string) (challengeBody, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = remoteAddr
	if session != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: session})
	}
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("%s: got %d", target, rec.Code)
	}
	var b challengeBody
	json.NewDecoder(rec.Body).Decode(&b)
	set := ""
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			set = c.Value
		}
	}
	return b, set
}

func TestReusesPendingOfferOnlyForSameSession(t *testing.T) {
	f := newFixture(t)
	node := f.countInvoices()
	const alice, bob = "203.0.113.1:1", "203.0.113.1:2" // same IP, two clients
	first, sess := f.offerFor(t, alice, "/premium/a", "")
	if sess == "" || node.n.Load() != 1 {
		t.Fatalf("expected a session cookie and one invoice (%d)", node.n.Load())
	}
	again, _ := f.offerFor(t, alice, "/premium/a", sess)
	if again.L402.Token != first.L402.Token || again.L402.Invoice != first.L402.Invoice || node.n.Load() != 1 {
		t.Fatalf("same session: expected the same offer and one invoice, got %d invoices", node.n.Load())
	}
	// Another client behind the same IP, without the cookie: its own offer.
	if other, set := f.offerFor(t, bob, "/premium/a", ""); other.L402.Token == first.L402.Token || set == sess || node.n.Load() != 2 {
		t.Fatal("a client without the session cookie must get its own offer")
	}
	// A forged or foreign cookie value does not give the offer either.
	if other, _ := f.offerFor(t, bob, "/premium/a", strings.Repeat("0", 32)); other.L402.Token == first.L402.Token {
		t.Fatal("another session must not get the offer")
	}
	// The cookie only counts from the same IP.
	if other, _ := f.offerFor(t, "198.51.100.7:1", "/premium/a", sess); other.L402.Token == first.L402.Token {
		t.Fatal("the session cookie must not carry an offer to another IP")
	}
	if other, _ := f.offerFor(t, alice, "/premium/b", sess); other.L402.Token == first.L402.Token {
		t.Fatal("another page needs another offer")
	}

	// Once used, the offer is not handed out again.
	if rec := f.get("/premium/a", f.pay(t, first), ""); rec.Code != http.StatusOK {
		t.Fatalf("pay: got %d", rec.Code)
	}
	if next, _ := f.offerFor(t, alice, "/premium/a", sess); next.L402.Token == first.L402.Token {
		t.Fatal("spent offer was reused")
	}

	// An offer past half of its lifetime is replaced by a fresh one.
	old, _ := f.offerFor(t, alice, "/premium/c", sess)
	f.now = f.now.Add(31 * time.Second) // TTL is one minute
	if fresh, _ := f.offerFor(t, alice, "/premium/c", sess); fresh.L402.Token == old.L402.Token {
		t.Fatal("stale offer was reused")
	}
}

func TestSessionCookie(t *testing.T) {
	f := newFixture(t)
	// Attributes: HttpOnly, SameSite=Lax, Secure only over TLS.
	for _, tls := range []bool{false, true} {
		target := "/premium/a"
		if tls {
			target = "https://example.com/premium/a" // sets req.TLS
		}
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		f.gate.ServeHTTP(rec, req)
		h := rec.Header().Get("Set-Cookie")
		if !strings.Contains(h, SessionCookie+"=") || !strings.Contains(h, "HttpOnly") || !strings.Contains(h, "SameSite=Lax") ||
			strings.Contains(h, "Secure") != tls {
			t.Fatalf("tls=%v: Set-Cookie %q", tls, h)
		}
	}
	// No cookie on free pages; the session cookie never reaches the origin.
	var seen string
	f.gate.opt.Next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Get("Cookie") })
	req := httptest.NewRequest(http.MethodGet, "/free", nil)
	req.Header.Set("Cookie", "a=1; "+SessionCookie+"="+strings.Repeat("ab", 16)+"; b=2")
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	if seen != "a=1; b=2" || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("origin saw Cookie %q, Set-Cookie %q", seen, rec.Header().Get("Set-Cookie"))
	}
}

// Clients without a cookie get a new offer per request, but the per-IP
// limit and the global invoice rate still bound the node calls.
func TestNoCookieStillBoundedByNodeGuards(t *testing.T) {
	f := newFixture(t)
	node := f.countInvoices()
	f.gate.limiter = newRateLimiter(30)
	f.gate.node = newNodeGuard(node, 60) // 1/s, burst 5
	codes := map[int]int{}
	for i := 0; i < 2000; i++ {
		rec := f.getFrom(fmt.Sprintf("10.0.%d.%d:1", i/200, i%200), fmt.Sprintf("/premium/%d", i%3))
		codes[rec.Code]++
	}
	for i := 0; i < 100; i++ { // one IP alone
		rec := f.getFrom("192.0.2.1:1", "/premium/x")
		codes[rec.Code]++
	}
	if node.n.Load() > 5 || codes[http.StatusTooManyRequests] != 70 {
		t.Fatalf("node calls %d (want <= 5), statuses %v", node.n.Load(), codes)
	}
}

func TestPendingInvoiceCap(t *testing.T) {
	f := newFixture(t)
	node := f.countInvoices()
	f.gate.pending = newPendingOffers(2)
	f.challenge(t, "/premium/a")
	f.challenge(t, "/premium/b")
	b := f.challenge(t, "/premium/c")
	if b.L402 != nil || node.n.Load() != 2 {
		t.Fatalf("over the cap: expected no invoice, got %d invoices", node.n.Load())
	}
	if rec := f.get("/premium/c", solve(t, b), ""); rec.Code != http.StatusOK {
		t.Fatalf("PoW must still work over the cap: got %d", rec.Code)
	}
	// Expired offers free their slot.
	f.now = f.now.Add(time.Minute)
	if b := f.challenge(t, "/premium/d"); b.L402 == nil {
		t.Fatal("expected an invoice once old ones expired")
	}
	if n := len(f.gate.pending.offers); n != 1 {
		t.Fatalf("expired offers not evicted: %d left", n)
	}
}

func TestExpectedSolveTime(t *testing.T) {
	f := newFixture(t)
	b := f.challenge(t, "/premium/a")
	if b.PoW.ExpectedSeconds != 0.0 { // difficulty 8: 256 hashes, rounds to 0.0 s
		t.Fatalf("got %g", b.PoW.ExpectedSeconds)
	}
	f.pow.Difficulty = 24
	if b := f.challenge(t, "/premium/b"); b.PoW.ExpectedSeconds != 8.4 { // 2^24 / 2e6
		t.Fatalf("got %g", b.PoW.ExpectedSeconds)
	}
}

func TestClientKey(t *testing.T) {
	_, proxies, _ := net.ParseCIDR("10.0.0.0/8")
	trusted := []*net.IPNet{proxies}
	cases := []struct {
		name, remote, header, value, want string
	}{
		{"plain IPv4", "192.0.2.7:5555", "", "", "192.0.2.7"},
		{"IPv6 grouped by /64", "[2001:db8::1]:5555", "", "", "2001:db8::/64"},
		{"IPv4-mapped IPv6", "[::ffff:192.0.2.7]:5555", "", "", "192.0.2.7"},
		{"header ignored unless configured", "192.0.2.7:5555", "", "6.6.6.6", "192.0.2.7"},
		{"header from untrusted peer ignored", "192.0.2.7:5555", "X-Forwarded-For", "6.6.6.6", "192.0.2.7"},
		{"header from trusted proxy", "10.0.0.1:5555", "X-Real-IP", "192.0.2.8", "192.0.2.8"},
		{"spoofed first hop ignored", "10.0.0.1:5555", "X-Forwarded-For", "6.6.6.6, 192.0.2.9", "192.0.2.9"},
		{"chain of trusted proxies skipped", "10.0.0.1:5555", "X-Forwarded-For", "6.6.6.6, 192.0.2.9, 10.0.0.2", "192.0.2.9"},
		{"garbage stops the walk", "10.0.0.1:5555", "X-Forwarded-For", "192.0.2.9, junk, 10.0.0.2", "10.0.0.2"},
		{"header missing", "10.0.0.1:5555", "X-Real-IP", "", "10.0.0.1"},
		{"header garbage", "10.0.0.1:5555", "X-Real-IP", "not-an-ip", "10.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = c.remote
		h := c.header
		if c.value != "" {
			name := h
			if name == "" {
				name = "X-Forwarded-For"
			}
			r.Header.Set(name, c.value)
		}
		if got := clientKey(r, h, trusted); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSpoofedHeaderCannotDodgeRateLimit(t *testing.T) {
	f := newFixture(t)
	_, proxies, _ := net.ParseCIDR("10.0.0.0/8")
	f.gate.opt.ClientIPHeader = "X-Forwarded-For"
	f.gate.opt.TrustedProxies = []*net.IPNet{proxies}
	f.gate.limiter = newRateLimiter(2)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/premium/a", nil)
		req.RemoteAddr = "192.0.2.7:1000" // not a trusted proxy
		req.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(i))
		rec := httptest.NewRecorder()
		f.gate.ServeHTTP(rec, req)
		if i == 2 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("spoofed X-Forwarded-For bypassed the limit: got %d", rec.Code)
		}
	}
}

func TestSpentSetBounded(t *testing.T) {
	s := newSpentSet()
	s.max = 2
	now := time.Unix(0, 0)
	if s.use("a", now.Add(time.Minute), now) != nil || s.use("b", now.Add(time.Minute), now) != nil {
		t.Fatal("first uses must succeed")
	}
	if err := s.use("c", now.Add(time.Minute), now); err != errBusy {
		t.Fatalf("full set: got %v, want errBusy", err)
	}
	if err := s.use("a", now.Add(time.Minute), now); err != errReplay {
		t.Fatalf("replay: got %v", err)
	}
	later := now.Add(2 * time.Minute)
	if err := s.use("c", later.Add(time.Minute), later); err != nil {
		t.Fatalf("after expiry: got %v", err)
	}
	if len(s.ids) != 1 {
		t.Fatalf("expired entries kept: %d", len(s.ids))
	}
}
