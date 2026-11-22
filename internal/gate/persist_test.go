package gate

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// slowStore blocks every Persist until release is closed.
type slowStore struct {
	entered chan struct{}
	release chan struct{}
	fail    atomic.Bool
}

func (s *slowStore) Persist(persist.SpentRecord) error {
	s.entered <- struct{}{}
	<-s.release
	if s.fail.Load() {
		return errors.New("disk full")
	}
	return nil
}

// A slow disk write must not hold the set's lock: other lookups proceed,
// and a concurrent use of the same token is still a replay.
func TestSpentDiskWriteDoesNotHoldLock(t *testing.T) {
	st := &slowStore{entered: make(chan struct{}, 4), release: make(chan struct{})}
	s := newSpentSet()
	s.store = st
	now := time.Unix(1_800_000_000, 0)
	exp := now.Add(time.Minute)
	id := "pay:" + strings.Repeat("ab", 16)

	done := make(chan error, 1)
	go func() { done <- s.use(id, exp, now) }()
	<-st.entered // the write is in flight

	lockFree := make(chan struct{})
	go func() {
		s.has("pay:" + strings.Repeat("cd", 16))
		s.len()
		close(lockFree)
	}()
	select {
	case <-lockFree:
	case <-time.After(2 * time.Second):
		t.Fatal("spent set is locked while the disk write is in flight")
	}
	if err := s.use(id, exp, now); err != errReplay {
		t.Fatalf("same token while writing: %v", err)
	}
	close(st.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.use(id, exp, now); err != errReplay {
		t.Fatalf("after: %v", err)
	}
}

// If the disk write fails the token is not spent: the caller is refused
// and can try again later.
func TestSpentFailedWriteIsNotSpent(t *testing.T) {
	st := &slowStore{entered: make(chan struct{}, 4), release: make(chan struct{})}
	close(st.release)
	st.fail.Store(true)
	s := newSpentSet()
	s.store = st
	now := time.Unix(1_800_000_000, 0)
	id := "pay:" + strings.Repeat("ef", 16)
	if err := s.use(id, now.Add(time.Minute), now); err != errBusy {
		t.Fatalf("got %v, want errBusy", err)
	}
	if s.has(id) {
		t.Fatal("failed write left the token spent")
	}
	st.fail.Store(false)
	if err := s.use(id, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
}

// Many concurrent redemptions: each token is accepted exactly once and
// every accepted token is still spent after a restart.
func TestSpentConcurrentUsesSurviveRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	key := []byte(strings.Repeat("k", 32))
	now := time.Unix(1_800_000_000, 0)
	st, err := persist.OpenSpent(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(now); err != nil {
		t.Fatal(err)
	}
	s := newSpentSet()
	s.store = st
	const n = 300
	var wg sync.WaitGroup
	var ok atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("pay:%032x", i%100) // 3 racers per token
			if s.use(id, now.Add(time.Hour), now) == nil {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 100 {
		t.Fatalf("%d uses accepted, want 100", ok.Load())
	}
	st2, err := persist.OpenSpent(dir, key)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := st2.Load(now)
	if err != nil || len(recs) != 100 {
		t.Fatalf("after restart: %d records, %v", len(recs), err)
	}
}
