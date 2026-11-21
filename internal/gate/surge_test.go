package gate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/pow"
)

func surgeOpts(on bool) SurgeConfig {
	return SurgeConfig{SurgeSettings: SurgeSettings{
		Enabled:             SurgeEnabled(on),
		MaxMultiplier:       3,
		ThresholdPerMinute:  20,
		Step:                0.25,
		StepIntervalMinutes: 2,
	}}
}

func surgeFixture(t *testing.T, sc SurgeConfig) *fixture {
	t.Helper()
	f := newFixture(t)
	opt := f.gate.opt
	opt.Protect = []string{"/articles/**", "/premium/**"}
	opt.PriceSats = 10
	opt.Prices = []PriceRule{
		{Pattern: "/articles/**", PriceSats: 5},
		{Pattern: "/premium/**", PriceSats: 20},
	}
	opt.Surge = sc
	opt.ChallengeTTL = 10 * time.Minute
	f.gate = New(opt)
	f.gate.now = func() time.Time { return f.now }
	return f
}

func fire(t *testing.T, f *fixture, prefix string, n int, start int) {
	t.Helper()
	for i := 0; i < n; i++ {
		rec := f.get(fmt.Sprintf("%s/p%d", prefix, start+i), "", "")
		if rec.Code != http.StatusPaymentRequired {
			t.Fatalf("%s #%d: got %d %q", prefix, start+i, rec.Code, rec.Body.String())
		}
	}
}

func priceOf(t *testing.T, f *fixture, path string) int64 {
	t.Helper()
	return f.challenge(t, path).PriceSats
}

func TestSurgeOffByDefault(t *testing.T) {
	f := newFixture(t)
	fire(t, f, "/premium", 200, 0)
	if p := priceOf(t, f, "/premium/after"); p != 10 {
		t.Fatalf("price %d, want 10 (surge off)", p)
	}
	if st := f.gate.Status(); len(st.Surge) != 0 {
		t.Fatalf("status surge: %+v", st.Surge)
	}
}

func TestSurgeIgnoresShortSpike(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	fire(t, f, "/premium", 30, 0) // ~7/min, well under 20
	f.now = f.now.Add(2*time.Minute + time.Second)
	if p := priceOf(t, f, "/premium/later"); p != 20 {
		t.Fatalf("short spike moved the price: %d", p)
	}
}

func TestSurgeRisesUnderSustainedLoadAndIsCapped(t *testing.T) {
	sc := surgeOpts(true)
	sc.MaxMultiplier = 1.5
	f := surgeFixture(t, sc)

	start := f.now
	// One request per second. 60 402s/min with a 20/min threshold is
	// clearly "sustained". Time only moves forward.
	drive := func(from, to int) {
		t.Helper()
		for i := from; i < to; i++ {
			f.now = start.Add(time.Duration(i) * time.Second)
			rec := f.get(fmt.Sprintf("/premium/p%d", i), "", "")
			if rec.Code != http.StatusPaymentRequired {
				t.Fatalf("t=%s #%d: %d", f.now.Sub(start), i, rec.Code)
			}
		}
	}
	drive(0, 121)
	if p := priceOf(t, f, "/premium/mid"); p != 25 { // ceil(20 * 1.25)
		t.Fatalf("after first interval: %d, want 25", p)
	}
	drive(121, 241)
	if p := priceOf(t, f, "/premium/cap"); p != 30 { // ceil(20 * 1.5)
		t.Fatalf("at cap: %d, want 30", p)
	}
	drive(241, 361)
	if p := priceOf(t, f, "/premium/still"); p != 30 {
		t.Fatalf("above cap: %d, want 30", p)
	}
}

func TestSurgeDecaysBackToBase(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	start := f.now
	for i := 0; i < 150; i++ {
		f.now = start.Add(time.Duration(i) * time.Second)
		if rec := f.get(fmt.Sprintf("/premium/p%d", i), "", ""); rec.Code != http.StatusPaymentRequired {
			t.Fatalf("%d", rec.Code)
		}
	}
	f.now = start.Add(121 * time.Second)
	if p := priceOf(t, f, "/premium/high"); p != 25 {
		t.Fatalf("expected a rise first: %d", p)
	}
	// No more demand; wait long enough for the rate to fall and the
	// multiplier to step back to 1× (one step every 2 minutes).
	f.now = start.Add(20 * time.Minute)
	if p := priceOf(t, f, "/premium/quiet"); p != 20 {
		t.Fatalf("did not decay: %d", p)
	}
}

func TestSurgeGroupsAreIndependent(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	start := f.now
	for i := 0; i < 150; i++ {
		f.now = start.Add(time.Duration(i) * time.Second)
		if rec := f.get(fmt.Sprintf("/articles/p%d", i), "", ""); rec.Code != http.StatusPaymentRequired {
			t.Fatalf("%d", rec.Code)
		}
	}
	f.now = start.Add(121 * time.Second)
	if p := priceOf(t, f, "/articles/now"); p != 7 { // ceil(5 * 1.25)
		t.Fatalf("articles: %d, want 7", p)
	}
	if p := priceOf(t, f, "/premium/untouched"); p != 20 {
		t.Fatalf("premium moved without demand: %d", p)
	}
}

func TestSurgeTokenBoundPriceHonouredAfterChange(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	paid := f.pay(t, f.challenge(t, "/premium/keep"))
	work := solve(t, f.challenge(t, "/premium/work"))
	start := f.now
	for i := 0; i < 150; i++ {
		f.now = start.Add(time.Duration(i) * time.Second)
		f.get(fmt.Sprintf("/premium/p%d", i), "", "")
	}
	f.now = start.Add(121 * time.Second)
	if p := priceOf(t, f, "/premium/now"); p != 25 {
		t.Fatalf("expected rise: %d", p)
	}
	if rec := f.get("/premium/keep", paid, ""); rec.Code != http.StatusOK {
		t.Fatalf("paid invoice at the quoted price: %d %q", rec.Code, rec.Body.String())
	}
	rec := f.get("/premium/work", work, "")
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), errWrongPrice.Error()) {
		t.Fatalf("old PoW after surge: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSurgePoWScalesWithMultiplier(t *testing.T) {
	sc := surgeOpts(true)
	f := newFixture(t)
	opt := f.gate.opt
	opt.Protect = []string{"/premium/**"}
	opt.PriceSats = 10
	opt.Surge = sc
	opt.ChallengeTTL = 10 * time.Minute
	opt.PoW = func(price int64) pow.Params {
		return pow.ForPrice(pow.SHA256d, 0, price, 1)
	}
	f.gate = New(opt)
	f.gate.now = func() time.Time { return f.now }

	base := f.challenge(t, "/premium/base")
	start := f.now
	for i := 0; i < 150; i++ {
		f.now = start.Add(time.Duration(i) * time.Second)
		f.get(fmt.Sprintf("/premium/p%d", i), "", "")
	}
	f.now = start.Add(121 * time.Second)
	up := f.challenge(t, "/premium/up")
	if up.PriceSats != 13 { // ceil(10 * 1.25)
		t.Fatalf("price %d, want 13", up.PriceSats)
	}
	if up.PoW.Difficulty <= base.PoW.Difficulty {
		t.Fatalf("PoW did not get harder: base=%d up=%d (prices %d → %d)",
			base.PoW.Difficulty, up.PoW.Difficulty, base.PriceSats, up.PriceSats)
	}
}

func TestSurgePressureBumpsMultiplier(t *testing.T) {
	sc := surgeOpts(true)
	sc.ThresholdPerMinute = 10_000 // demand alone will not move the price
	f := surgeFixture(t, sc)
	// One 402 opens the group and starts the step clock.
	if p := priceOf(t, f, "/premium/first"); p != 20 {
		t.Fatalf("start %d", p)
	}
	f.gate.node.mu.Lock()
	f.gate.node.openUntil = f.now.Add(10 * time.Minute)
	f.gate.node.mu.Unlock()
	f.now = f.now.Add(2*time.Minute + time.Second)
	if p := priceOf(t, f, "/premium/pressured"); p != 25 {
		t.Fatalf("breaker open should have stepped the price: %d", p)
	}
}

func TestSurgeGroupOverrideOff(t *testing.T) {
	sc := surgeOpts(true)
	sc.Groups = map[string]SurgeSettings{
		"/premium/**": {Enabled: SurgeEnabled(false)},
	}
	f := surgeFixture(t, sc)
	start := f.now
	for i := 0; i < 150; i++ {
		f.now = start.Add(time.Duration(i) * time.Second)
		f.get(fmt.Sprintf("/premium/p%d", i), "", "")
		f.get(fmt.Sprintf("/articles/p%d", i), "", "")
	}
	f.now = start.Add(121 * time.Second)
	if p := priceOf(t, f, "/premium/still"); p != 20 {
		t.Fatalf("opted-out group moved: %d", p)
	}
	if p := priceOf(t, f, "/articles/now"); p != 7 {
		t.Fatalf("articles should still surge: %d", p)
	}
}

func TestSurgeStatusAndRounding(t *testing.T) {
	if applySurge(10, 1.25) != 13 || applySurge(10, 1) != 10 || applySurge(1, 1.25) != 2 {
		t.Fatalf("applySurge: 10@1.25=%d 1@1.25=%d", applySurge(10, 1.25), applySurge(1, 1.25))
	}
	f := surgeFixture(t, surgeOpts(true))
	st := f.gate.Status()
	if len(st.Surge) < 2 {
		t.Fatalf("expected both groups in status: %+v", st.Surge)
	}
	raw, _ := json.Marshal(st.Surge)
	if !strings.Contains(string(raw), `"group":"/premium/**"`) || !strings.Contains(string(raw), `"multiplier":1`) {
		t.Fatalf("status json: %s", raw)
	}
}

func TestSurgeRateMath(t *testing.T) {
	// Instant N events: rate = N * ln2 / halfLifeMinutes.
	s := newSurger(surgeOpts(true))
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < 30; i++ {
		s.hit("/premium/**", now, false)
	}
	s.mu.Lock()
	g := s.groups["/premium/**"]
	s.decayLocked(g, now)
	rate := s.rateLocked(g)
	s.mu.Unlock()
	want := 30 * surgeLn2 / surgeHalfLife.Minutes()
	if rate < want*0.99 || rate > want*1.01 {
		t.Fatalf("rate %g, want ~%g", rate, want)
	}
}
