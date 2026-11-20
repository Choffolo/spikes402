package gate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/lightning"
)

// scriptedNode is a Lightning backend whose behaviour tests control.
type scriptedNode struct {
	calls   atomic.Int64
	fail    atomic.Bool
	block   chan struct{} // if set, calls wait for it or for their deadline
	started chan struct{} // if set, receives one value per call that started
}

func (n *scriptedNode) CreateInvoice(ctx context.Context, _ int64, _ string, _ time.Duration) (lightning.Invoice, error) {
	n.calls.Add(1)
	if n.started != nil {
		n.started <- struct{}{}
	}
	if n.block != nil {
		select {
		case <-n.block:
		case <-ctx.Done():
			return lightning.Invoice{}, ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return lightning.Invoice{}, ctx.Err()
	}
	if n.fail.Load() {
		return lightning.Invoice{}, errors.New("node error")
	}
	return lightning.NewFake().CreateInvoice(ctx, 1, "", time.Minute)
}

func TestGlobalInvoiceRate(t *testing.T) {
	f := newFixture(t)
	f.gate.node = newNodeGuard(f.ln, 60) // 1 per second, bursts of 5
	for i := 0; i < 5; i++ {
		if b := f.challenge(t, "/premium/"+strconv.Itoa(i)); b.L402 == nil {
			t.Fatalf("invoice %d: expected an invoice within the burst", i)
		}
	}
	b := f.challenge(t, "/premium/over")
	if b.L402 != nil || b.PoW.Challenge == "" {
		t.Fatal("over the global rate: expected proof-of-work only")
	}
	if rec := f.get("/premium/over", solve(t, b), ""); rec.Code != http.StatusOK {
		t.Fatalf("PoW must still work: got %d", rec.Code)
	}
	f.now = f.now.Add(time.Second)
	if b := f.challenge(t, "/premium/later"); b.L402 == nil {
		t.Fatal("one more invoice expected after a second")
	}
}

func TestNodeTimeout(t *testing.T) {
	node := &scriptedNode{block: make(chan struct{})} // never answers
	g := newNodeGuard(node, 600)
	g.timeout = 50 * time.Millisecond
	start := time.Now()
	if _, err := g.createInvoice(time.Now(), 1, "", time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("timeout not applied")
	}
}

func TestNodeConcurrencyCap(t *testing.T) {
	node := &scriptedNode{block: make(chan struct{}), started: make(chan struct{}, nodeMaxInFlight)}
	g := newNodeGuard(node, 6000)
	var wg sync.WaitGroup
	for i := 0; i < nodeMaxInFlight; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); g.createInvoice(time.Now(), 1, "", time.Minute) }()
	}
	for i := 0; i < nodeMaxInFlight; i++ {
		<-node.started
	}
	if _, err := g.createInvoice(time.Now(), 1, "", time.Minute); err != errNodeBusy {
		t.Fatalf("got %v, want errNodeBusy", err)
	}
	close(node.block)
	wg.Wait()
	if node.calls.Load() != nodeMaxInFlight {
		t.Fatalf("node called %d times", node.calls.Load())
	}
}

func TestCircuitBreaker(t *testing.T) {
	node := &scriptedNode{}
	node.fail.Store(true)
	g := newNodeGuard(node, 6000)
	now := time.Unix(0, 0)
	for i := 0; i < breakerThreshold; i++ {
		g.createInvoice(now, 1, "", time.Minute)
	}
	if _, err := g.createInvoice(now, 1, "", time.Minute); err != errNodeBreaker {
		t.Fatalf("got %v, want errNodeBreaker", err)
	}
	if node.calls.Load() != breakerThreshold {
		t.Fatalf("node called %d times while the breaker was open", node.calls.Load())
	}

	// After the cooldown one trial call; a failure re-opens immediately.
	now = now.Add(breakerCooldown)
	g.createInvoice(now, 1, "", time.Minute)
	if _, err := g.createInvoice(now, 1, "", time.Minute); err != errNodeBreaker {
		t.Fatalf("after failed trial: got %v", err)
	}

	// A successful trial closes the breaker.
	node.fail.Store(false)
	now = now.Add(breakerCooldown)
	for i := 0; i < 3; i++ {
		if _, err := g.createInvoice(now, 1, "", time.Minute); err != nil {
			t.Fatalf("healthy node: got %v", err)
		}
	}
}

func TestBreakerOffersPoWOnly(t *testing.T) {
	f := newFixture(t)
	node := &scriptedNode{}
	node.fail.Store(true)
	f.gate.node.backend = node
	for i := 0; i < breakerThreshold+3; i++ {
		if b := f.challenge(t, "/premium/"+strconv.Itoa(i)); b.L402 != nil || b.PoW.Challenge == "" {
			t.Fatal("expected proof-of-work only")
		}
	}
	if node.calls.Load() != breakerThreshold {
		t.Fatalf("node called %d times, want %d", node.calls.Load(), breakerThreshold)
	}
}

func TestVisitorHangingUpIsNotANodeFailure(t *testing.T) {
	f := newFixture(t)
	f.gate.node.backend = &scriptedNode{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < breakerThreshold+1; i++ {
		req := httptest.NewRequest(http.MethodGet, "/premium/"+strconv.Itoa(i), nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		f.gate.ServeHTTP(rec, req)
		if len(rec.Header().Values("WWW-Authenticate")) != 2 {
			t.Fatalf("request %d: expected an invoice despite the cancelled request", i)
		}
	}
}
