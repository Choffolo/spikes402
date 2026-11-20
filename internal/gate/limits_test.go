package gate

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestReusesPendingOffer(t *testing.T) {
	f := newFixture(t)
	node := f.countInvoices()
	first := f.challenge(t, "/premium/a")
	again := f.challenge(t, "/premium/a")
	if again.L402.Token != first.L402.Token || again.L402.Invoice != first.L402.Invoice || node.n.Load() != 1 {
		t.Fatalf("expected the same offer and one invoice, got %d invoices", node.n.Load())
	}
	if other := f.challenge(t, "/premium/b"); other.L402.Token == first.L402.Token {
		t.Fatal("another page needs another offer")
	}
	if rec := f.getFrom("203.0.113.9:1", "/premium/a"); rec.Code != http.StatusPaymentRequired || node.n.Load() != 3 {
		t.Fatalf("another client needs another offer (%d invoices)", node.n.Load())
	}

	// Once used, the offer is not handed out again.
	if rec := f.get("/premium/a", f.pay(t, first), ""); rec.Code != http.StatusOK {
		t.Fatalf("pay: got %d", rec.Code)
	}
	if next := f.challenge(t, "/premium/a"); next.L402.Token == first.L402.Token {
		t.Fatal("spent offer was reused")
	}

	// An offer past half of its lifetime is replaced by a fresh one.
	old := f.challenge(t, "/premium/c")
	f.now = f.now.Add(31 * time.Second) // TTL is one minute
	if fresh := f.challenge(t, "/premium/c"); fresh.L402.Token == old.L402.Token {
		t.Fatal("stale offer was reused")
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
