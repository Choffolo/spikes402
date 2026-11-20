package gate

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
)

// Protection of the Lightning node against the paywall's own traffic.
// Every limit here fails "closed" for invoices and "open" for visitors:
// when the node may not be called, the 402 answer simply offers
// proof-of-work only.
const (
	nodeTimeout      = 3 * time.Second  // a single invoice request may take this long
	nodeMaxInFlight  = 8                // concurrent invoice requests to the node
	breakerThreshold = 5                // consecutive failures that trip the breaker
	breakerCooldown  = 30 * time.Second // how long the node is left alone after that
)

var (
	errNodeRate    = errors.New("invoice rate limit reached")
	errNodeBusy    = errors.New("too many invoice requests in flight")
	errNodeBreaker = errors.New("lightning node failing, paused")
)

// nodeGuard wraps a Lightning backend with a global rate limit (token
// bucket), a concurrency cap, a timeout and a circuit breaker.
type nodeGuard struct {
	backend  lightning.Backend
	timeout  time.Duration
	inFlight chan struct{}

	mu        sync.Mutex
	rate      float64 // invoices per second
	burst     float64
	tokens    float64
	last      time.Time
	failures  int       // consecutive failures
	tripped   bool      // breaker opened at least once since the last success
	openUntil time.Time // no calls before this time

	created, failed uint64 // totals, for the dashboard
	lastErr         string
	lastErrAt       time.Time
}

// newNodeGuard allows perMinute invoices per minute, spread evenly, with
// bursts of up to five seconds' worth.
func newNodeGuard(b lightning.Backend, perMinute int) *nodeGuard {
	rate := float64(perMinute) / 60
	burst := max(1, rate*5)
	return &nodeGuard{
		backend:  b,
		timeout:  nodeTimeout,
		inFlight: make(chan struct{}, nodeMaxInFlight),
		rate:     rate,
		burst:    burst,
		tokens:   burst,
	}
}

// createInvoice asks the node for an invoice if the limits allow it.
// It deliberately does not use the visitor's request context: a visitor
// hanging up must not count as a node failure (that would let anyone trip
// the breaker). The timeout still bounds every call.
func (n *nodeGuard) createInvoice(now time.Time, amountSat int64, memo string, expiry time.Duration) (lightning.Invoice, error) {
	if err := n.admit(now); err != nil {
		return lightning.Invoice{}, err
	}
	select {
	case n.inFlight <- struct{}{}:
		defer func() { <-n.inFlight }()
	default:
		return lightning.Invoice{}, errNodeBusy
	}

	ctx, cancel := context.WithTimeout(context.Background(), n.timeout)
	defer cancel()
	inv, err := n.backend.CreateInvoice(ctx, amountSat, memo, expiry)
	n.record(now, err)
	return inv, err
}

// admit applies the circuit breaker and the global rate limit.
func (n *nodeGuard) admit(now time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if now.Before(n.openUntil) {
		return errNodeBreaker
	}
	if !n.last.IsZero() {
		n.tokens = min(n.burst, n.tokens+now.Sub(n.last).Seconds()*n.rate)
	}
	n.last = now
	if n.tokens < 1 {
		return errNodeRate
	}
	n.tokens--
	return nil
}

// record updates the circuit breaker with the outcome of a call. After a
// cooldown, a single further failure opens the breaker again.
func (n *nodeGuard) record(now time.Time, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err == nil {
		n.created++
		n.failures, n.tripped = 0, false
		return
	}
	n.failed++
	n.lastErr, n.lastErrAt = sanitize(err.Error()), now
	n.failures++
	if n.tripped || n.failures >= breakerThreshold {
		n.tripped = true
		n.failures = 0
		n.openUntil = now.Add(breakerCooldown)
	}
}

func (n *nodeGuard) status(now time.Time) NodeStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := NodeStatus{
		BreakerOpen:         now.Before(n.openUntil),
		ConsecutiveFailures: n.failures,
		InFlight:            len(n.inFlight),
		InvoiceBudget:       n.tokens,
		InvoicesCreated:     n.created,
		InvoiceFailures:     n.failed,
		LastError:           n.lastErr,
		LastErrorAt:         n.lastErrAt,
	}
	if !n.last.IsZero() {
		st.InvoiceBudget = min(n.burst, n.tokens+now.Sub(n.last).Seconds()*n.rate)
	}
	if st.BreakerOpen {
		st.BreakerOpenUntil = n.openUntil
	}
	return st
}
