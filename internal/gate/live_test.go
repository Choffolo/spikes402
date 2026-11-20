package gate

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Choffolo/spikes402/internal/botid"
	"github.com/Choffolo/spikes402/pow"
)

type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) observe(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) outcomes() []Outcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Outcome
	for _, e := range r.events {
		out = append(out, e.Outcome)
	}
	r.events = nil
	return out
}

func expect(t *testing.T, rec *recorder, want ...Outcome) {
	t.Helper()
	got := rec.outcomes()
	if strings.Join(toStrings(got), ",") != strings.Join(toStrings(want), ",") {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
}

func toStrings(o []Outcome) (s []string) {
	for _, x := range o {
		s = append(s, string(x))
	}
	return s
}

func TestEvents(t *testing.T) {
	f := newFixture(t)
	withBots(f, nil)
	rec := &recorder{}
	f.gate.opt.Observer = rec.observe

	f.get("/free/page", "", "") // unprotected: not an event
	expect(t, rec)

	b := f.challenge(t, "/premium/a")
	expect(t, rec, OutcomeChallenged)
	f.get("/premium/a", solve(t, b), "")
	expect(t, rec, OutcomePoW)
	b = f.challenge(t, "/premium/b")
	f.get("/premium/b", f.pay(t, b), "")
	expect(t, rec, OutcomeChallenged, OutcomePaid)
	f.get("/premium/b", f.pay(t, f.challenge(t, "/premium/b"))+"0", "")
	expect(t, rec, OutcomeChallenged, OutcomeRejected)

	f.setPolicies(map[string]Policy{"impostor": {Action: ActionBlock}, "verified": {Action: ActionFree}})
	f.do("/premium/a", "6.6.6.6", gptUA, "")
	f.do("/premium/a", "20.0.0.5", gptUA, "")
	rec.mu.Lock()
	ev := rec.events
	rec.mu.Unlock()
	if len(ev) != 2 || ev[0].Class.Kind != botid.Impostor || ev[0].Client != "6.6.6.6" || ev[0].Resource != "GET /premium/a" || ev[1].Class.Name != "GPTBot" {
		t.Fatalf("events %+v", ev)
	}
	expect(t, rec, OutcomeBlocked, OutcomeFree)

	f.gate.limiter = newRateLimiter(1)
	f.challenge(t, "/premium/c")
	f.get("/premium/c", "", "")
	expect(t, rec, OutcomeChallenged, OutcomeRateLimited)

	// Argon2id checks full: busy.
	f.pow = pow.Params{Algorithm: pow.Argon2id, Difficulty: 1, MemoryMiB: 8}
	f.gate.limiter = newRateLimiter(100)
	b = f.challenge(t, "/premium/d")
	for i := 0; i < cap(f.gate.argonSlots); i++ {
		f.gate.argonSlots <- struct{}{}
	}
	if r := f.get("/premium/d", solve(t, b), ""); r.Code != http.StatusServiceUnavailable {
		t.Fatal(r.Code)
	}
	expect(t, rec, OutcomeChallenged, OutcomeBusy)
}

func TestUpdateSettings(t *testing.T) {
	f := newFixture(t)
	s := f.gate.Settings()
	if s.PriceSats != 10 || s.ChallengesPerMinute != 1000 {
		t.Fatalf("got %+v", s)
	}
	s.PriceSats, s.ChallengesPerMinute = 42, 1
	if err := f.gate.UpdateSettings(s); err != nil {
		t.Fatal(err)
	}
	if b := f.challenge(t, "/premium/a"); b.PriceSats != 42 || !strings.HasPrefix(b.L402.Invoice, "lnfake42sat") {
		t.Fatalf("got %+v", b)
	}
	if r := f.get("/premium/a", "", ""); r.Code != http.StatusTooManyRequests {
		t.Fatalf("new rate limit not applied: %d", r.Code)
	}
	for _, bad := range []Settings{
		{PriceSats: 0, PoW: s.PoW, ChallengesPerMinute: 1},
		{PriceSats: 1, ChallengesPerMinute: 1},
		{PriceSats: 1, PoW: s.PoW, ChallengesPerMinute: 0},
		{PriceSats: 1, PoW: s.PoW, ChallengesPerMinute: 1, Policies: map[string]Policy{"x": {Action: "nope"}}},
	} {
		if f.gate.UpdateSettings(bad) == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if f.gate.Settings().PriceSats != 42 {
		t.Fatal("bad update changed the settings")
	}
}

func TestStatus(t *testing.T) {
	f := newFixture(t)
	node := &scriptedNode{}
	f.gate.node = newNodeGuard(node, 6000)
	b := f.challenge(t, "/premium/a")
	f.get("/premium/a", solve(t, b), "")
	st := f.gate.Status()
	if st.OpenInvoices != 0 || st.SpentTokens != 1 || st.Node.InvoicesCreated != 1 || st.Node.BreakerOpen {
		t.Fatalf("got %+v", st)
	}
	f.challenge(t, "/premium/b")
	if st := f.gate.Status(); st.OpenInvoices != 1 {
		t.Fatalf("got %+v", st)
	}
	node.fail.Store(true)
	for i := 0; i < breakerThreshold; i++ {
		f.challenge(t, "/premium/x"+string(rune('a'+i)))
	}
	st = f.gate.Status()
	if !st.Node.BreakerOpen || st.Node.InvoiceFailures != breakerThreshold || st.Node.LastError != "node error" {
		t.Fatalf("got %+v", st.Node)
	}
}
