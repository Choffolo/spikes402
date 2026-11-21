package gate

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/internal/persist"
)

func TestSpentStoreRestartReplay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	key := []byte(strings.Repeat("k", 32))
	now := time.Unix(1_800_000_000, 0)
	exp := now.Add(10 * time.Minute)

	st, err := persist.OpenSpent(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if recs, err := st.Load(now); err != nil || len(recs) != 0 {
		t.Fatalf("first run: %v %v", recs, err)
	}
	s := newSpentSet()
	s.store = st
	id := "pay:" + strings.Repeat("ab", 16)
	if err := s.use(id, exp, now); err != nil {
		t.Fatal(err)
	}
	if err := s.use(id, exp, now); err != errReplay {
		t.Fatalf("same process: %v", err)
	}

	// New set, same disk: the token must still be spent.
	st2, err := persist.OpenSpent(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := st2.Load(now)
	if err != nil {
		t.Fatal(err)
	}
	s2 := newSpentSet()
	s2.store = st2
	s2.load(recs)
	if err := s2.use(id, exp, now); err != errReplay {
		t.Fatalf("after restart: %v (loaded %d)", err, len(recs))
	}
}

func TestPendingInvoiceCapSurvivesPoWRedeem(t *testing.T) {
	f := newFixture(t)
	node := f.countInvoices()
	f.gate.pending = newPendingOffers(1)
	a := f.challenge(t, "/premium/a")
	if a.L402 == nil || node.n.Load() != 1 {
		t.Fatal("expected one invoice")
	}
	if rec := f.get("/premium/a", solve(t, a), ""); rec.Code != 200 {
		t.Fatalf("PoW redeem: %d", rec.Code)
	}
	// The offer is gone, but the invoice is still open until it expires.
	b := f.challenge(t, "/premium/b")
	if b.L402 != nil || node.n.Load() != 1 {
		t.Fatalf("PoW redeem must not free the invoice slot: invoices=%d l402=%v", node.n.Load(), b.L402 != nil)
	}
	f.now = f.now.Add(time.Minute)
	if c := f.challenge(t, "/premium/c"); c.L402 == nil || node.n.Load() != 2 {
		t.Fatal("expected an invoice once the first one expired")
	}
}

func TestPendingReserveIsAtomic(t *testing.T) {
	p := newPendingOffers(1)
	now := time.Unix(1_800_000_000, 0)
	var ok1, ok2 bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { ok1 = p.reserve(now); wg.Done() }()
	go func() { ok2 = p.reserve(now); wg.Done() }()
	wg.Wait()
	if ok1 == ok2 {
		t.Fatalf("exactly one reserve should succeed, got %v %v (reserved=%d)", ok1, ok2, p.reserved)
	}
	if p.reserved != 1 {
		t.Fatalf("reserved=%d", p.reserved)
	}
	p.cancelReserve()
	if p.reserved != 0 || !p.reserve(now) {
		t.Fatal("cancelReserve should free the slot")
	}
}
