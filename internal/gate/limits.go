package gate

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/pow"
)

// maxTrackedClients bounds the memory of the rate limiter. When more
// clients than this are active in the same minute, the newcomers share
// one common allowance (see rateLimiter.allow).
const maxTrackedClients = 100_000

// rateLimiter allows each client a fixed number of 402 answers per
// minute (a fixed one-minute window per client).
type rateLimiter struct {
	mu        sync.Mutex
	perMinute int
	clients   map[string]*window
	lastSweep time.Time
}

type window struct {
	start time.Time
	count int
}

func newRateLimiter(perMinute int) *rateLimiter {
	return &rateLimiter{perMinute: perMinute, clients: make(map[string]*window)}
}

func (l *rateLimiter) setPerMinute(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.perMinute = n
}

// allow counts one 402 answer for client. If the client is over its limit
// it returns false and how long to wait.
func (l *rateLimiter) allow(client string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= time.Minute { // forget finished windows
		for k, w := range l.clients {
			if now.Sub(w.start) >= time.Minute {
				delete(l.clients, k)
			}
		}
		l.lastSweep = now
	}

	w, ok := l.clients[client]
	if !ok && len(l.clients) >= maxTrackedClients {
		client = "overflow" // too many clients at once: share one allowance
		w, ok = l.clients[client]
	}
	if !ok || now.Sub(w.start) >= time.Minute {
		w = &window{start: now}
		l.clients[client] = w
	}
	if w.count >= l.perMinute {
		return false, w.start.Add(time.Minute).Sub(now)
	}
	w.count++
	return true, 0
}

// offer is one 402 challenge: a signed token, maybe with an invoice.
type offer struct {
	id      string
	token   string
	invoice string // "" when no invoice could be offered
	expires time.Time
	price   int64
	pow     pow.Params
}

// pendingOffers remembers the invoices handed out and not yet expired,
// per client and resource. A client asking again for the same page gets
// the same offer instead of a new invoice, and the total number of open
// invoices is capped.
type pendingOffers struct {
	mu        sync.Mutex
	max       int
	offers    map[string]offer // key: client + " " + resource
	lastSweep time.Time
}

func newPendingOffers(max int) *pendingOffers {
	return &pendingOffers{max: max, offers: make(map[string]offer)}
}

func (p *pendingOffers) len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.offers)
}

// get returns the pending offer for key if it still has at least
// minLeft before it expires.
func (p *pendingOffers) get(key string, now time.Time, minLeft time.Duration) (offer, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o, ok := p.offers[key]
	if !ok || o.expires.Sub(now) < minLeft {
		return offer{}, false
	}
	return o, true
}

// reserve reports whether there is room for one more open invoice.
func (p *pendingOffers) reserve(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked(now)
	return len(p.offers) < p.max
}

// put stores an offer with an invoice. It may exceed max by the few
// requests that raced past reserve at the same moment, never more.
func (p *pendingOffers) put(key string, o offer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.offers[key] = o
}

// drop forgets the offer for key if it is the one with this token ID.
func (p *pendingOffers) drop(key, id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if o, ok := p.offers[key]; ok && o.id == id {
		delete(p.offers, key)
	}
}

func (p *pendingOffers) sweepLocked(now time.Time) {
	// Sweeping is O(n): do it at most once a second.
	if now.Sub(p.lastSweep) < time.Second {
		return
	}
	for k, o := range p.offers {
		if !now.Before(o.expires) {
			delete(p.offers, k)
		}
	}
	p.lastSweep = now
}

// clientKey identifies the client for rate limiting: its IP address, or
// for IPv6 its /64 network (one home or server usually gets a whole /64).
//
// The header (e.g. "X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP") is
// only believed when the connection comes from one of the trusted
// proxies; otherwise anyone could pick their own IP and dodge the limits.
// The header is read from right to left, skipping trusted proxies, so a
// value the client wrote itself at the start of X-Forwarded-For is ignored.
func clientKey(r *http.Request, header string, trusted []*net.IPNet) string {
	ip := clientIP(r, header, trusted)
	if ip == nil {
		return r.RemoteAddr
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// clientIP is the client's address (see clientKey), or nil.
func clientIP(r *http.Request, header string, trusted []*net.IPNet) net.IP {
	ip := remoteIP(r)
	if header != "" && ip != nil && isTrusted(ip, trusted) {
		hops := strings.Split(strings.Join(r.Header.Values(header), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop := net.ParseIP(strings.TrimSpace(hops[i]))
			if hop == nil {
				break // garbage: keep the last good address
			}
			ip = hop
			if !isTrusted(hop, trusted) {
				break
			}
		}
	}
	return ip
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func isTrusted(ip net.IP, trusted []*net.IPNet) bool {
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
