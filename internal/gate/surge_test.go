package gate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Choffolo/spikes402/pow"
)

func surgeOpts(on bool) SurgeConfig {
	return SurgeConfig{SurgeSettings: SurgeSettings{
		Enabled:             SurgeEnabled(on),
		MaxMultiplier:       3,
		MinPerMinute:        60,
		SurgeRatio:          2,
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
	opt.ChallengeTTL = 6 * time.Hour
	f.gate = New(opt)
	f.gate.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) getCookie(target, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	f.gate.ServeHTTP(rec, req)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return SessionCookie + "=" + c.Value
		}
	}
	return ""
}

// driveRate sends 402s on path at perMin for duration, reusing one session
// (and therefore one invoice) so the test stays cheap.
func driveRate(t *testing.T, f *fixture, path string, perMin int, d time.Duration) {
	t.Helper()
	if perMin < 1 {
		f.now = f.now.Add(d)
		return
	}
	interval := time.Minute / time.Duration(perMin)
	n := int(d / interval)
	if n < 1 {
		n = 1
	}
	start := f.now
	rec := f.getCookie(path, "")
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("%s start: %d %q", path, rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(rec)
	for i := 1; i < n; i++ {
		f.now = start.Add(time.Duration(i) * interval)
		rec = f.getCookie(path, cookie)
		if rec.Code != http.StatusPaymentRequired {
			t.Fatalf("%s #%d: %d %q", path, i, rec.Code, rec.Body.String())
		}
	}
	f.now = start.Add(d)
}

func priceOf(t *testing.T, f *fixture, path string) int64 {
	t.Helper()
	return f.challenge(t, path).PriceSats
}

func groupStatus(t *testing.T, f *fixture, group string) SurgeGroupStatus {
	t.Helper()
	for _, g := range f.gate.Status().Surge {
		if g.Group == group {
			return g
		}
	}
	t.Fatalf("group %s not in status: %+v", group, f.gate.Status().Surge)
	return SurgeGroupStatus{}
}

func TestSurgeOffByDefault(t *testing.T) {
	f := newFixture(t)
	driveRate(t, f, "/premium/a", 120, 10*time.Minute)
	if p := priceOf(t, f, "/premium/after"); p != 10 {
		t.Fatalf("price %d, want 10 (surge off)", p)
	}
	if st := f.gate.Status(); len(st.Surge) != 0 {
		t.Fatalf("status surge: %+v", st.Surge)
	}
}

func TestSurgeSteadyTrafficNeverSurges(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	driveRate(t, f, "/premium/steady", 80, 90*time.Minute)
	st := groupStatus(t, f, "/premium/**")
	if !st.Warmed {
		t.Fatal("expected a warmed baseline")
	}
	if p := priceOf(t, f, "/premium/after"); p != 20 {
		t.Fatalf("steady 80/min surged: price %d, status %+v", p, st)
	}
}

func TestSurgeDoublingRaisesPrice(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	driveRate(t, f, "/premium/base", 60, time.Hour)
	if p := priceOf(t, f, "/premium/mid"); p != 20 {
		t.Fatalf("after warm-up, still 1×: %d", p)
	}
	// A bit more than 2× so the short-term EWMA can cross 2× baseline
	// (it approaches the new rate from below). A few minutes is enough
	// for one step; longer would take further steps.
	driveRate(t, f, "/premium/high", 150, 6*time.Minute)
	if p := priceOf(t, f, "/premium/now"); p < 25 {
		t.Fatalf("after sustained doubling: %d, want at least 25 (status %+v)", p, groupStatus(t, f, "/premium/**"))
	}
}

func TestSurgeBelowFloorNeverSurges(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	driveRate(t, f, "/premium/small", 10, time.Hour)
	driveRate(t, f, "/premium/small", 25, 20*time.Minute) // 2.5× but 25 < 60
	if p := priceOf(t, f, "/premium/after"); p != 20 {
		t.Fatalf("under the floor: %d", p)
	}
}

func TestSurgeIgnoresShortSpike(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	driveRate(t, f, "/premium/base", 60, time.Hour)
	// A brief burst on a new session/path, then wait a step interval.
	start := f.now
	for i := 0; i < 40; i++ {
		f.getCookie(fmt.Sprintf("/premium/spike-%d", i), "")
	}
	f.now = start.Add(2*time.Minute + time.Second)
	if p := priceOf(t, f, "/premium/later"); p != 20 {
		t.Fatalf("short spike moved the price: %d", p)
	}
}

func TestSurgeRisesUnderSustainedLoadAndIsCapped(t *testing.T) {
	sc := surgeOpts(true)
	sc.MaxMultiplier = 1.5
	f := surgeFixture(t, sc)
	driveRate(t, f, "/premium/base", 60, time.Hour)
	// At 180/min the short rate crosses 2× baseline after ~3 min, then
	// one step every 2 minutes. Check between the first and second step.
	driveRate(t, f, "/premium/high", 180, 4*time.Minute)
	if p := priceOf(t, f, "/premium/mid"); p != 25 {
		t.Fatalf("after first interval: %d, want 25", p)
	}
	driveRate(t, f, "/premium/high", 180, 3*time.Minute)
	if p := priceOf(t, f, "/premium/cap"); p != 30 {
		t.Fatalf("at cap: %d, want 30", p)
	}
	driveRate(t, f, "/premium/high", 180, 8*time.Minute)
	if p := priceOf(t, f, "/premium/still"); p != 30 {
		t.Fatalf("above cap: %d, want 30", p)
	}
}

func TestSurgeDecaysBackToBase(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	driveRate(t, f, "/premium/base", 60, time.Hour)
	driveRate(t, f, "/premium/high", 180, 8*time.Minute)
	if p := priceOf(t, f, "/premium/high-now"); p < 25 {
		t.Fatalf("expected a rise first: %d", p)
	}
	// Back to the usual rate; the short-term EWMA falls toward the baseline.
	driveRate(t, f, "/premium/base", 60, 20*time.Minute)
	if p := priceOf(t, f, "/premium/quiet"); p != 20 {
		t.Fatalf("did not decay: %d", p)
	}
}

func TestSurgeGroupsAreIndependent(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	driveRate(t, f, "/articles/base", 60, time.Hour)
	driveRate(t, f, "/articles/high", 180, 15*time.Minute)
	if p := priceOf(t, f, "/articles/now"); p < 7 {
		t.Fatalf("articles did not surge: %d", p)
	}
	if p := priceOf(t, f, "/premium/untouched"); p != 20 {
		t.Fatalf("premium moved without demand: %d", p)
	}
}

func TestSurgeTokenBoundPriceHonouredAfterChange(t *testing.T) {
	f := surgeFixture(t, surgeOpts(true))
	paid := f.pay(t, f.challenge(t, "/premium/keep"))
	work := solve(t, f.challenge(t, "/premium/work"))
	driveRate(t, f, "/premium/base", 60, time.Hour)
	driveRate(t, f, "/premium/high", 180, 8*time.Minute)
	if p := priceOf(t, f, "/premium/now"); p < 25 {
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
	opt.ChallengeTTL = 6 * time.Hour
	opt.PoW = func(price int64) pow.Params {
		return pow.ForPrice(pow.SHA256d, 0, price, 1)
	}
	f.gate = New(opt)
	f.gate.now = func() time.Time { return f.now }

	base := f.challenge(t, "/premium/base")
	driveRate(t, f, "/premium/warm", 60, time.Hour)
	driveRate(t, f, "/premium/high", 180, 8*time.Minute)
	up := f.challenge(t, "/premium/up")
	if up.PriceSats <= base.PriceSats {
		t.Fatalf("price did not rise: %d", up.PriceSats)
	}
	if up.PoW.Difficulty <= base.PoW.Difficulty {
		t.Fatalf("PoW did not get harder: base=%d up=%d (prices %d → %d)",
			base.PoW.Difficulty, up.PoW.Difficulty, base.PriceSats, up.PriceSats)
	}
}

func TestSurgePressureBumpsMultiplier(t *testing.T) {
	sc := surgeOpts(true)
	sc.MinPerMinute = 10_000 // demand alone will not move the price
	f := surgeFixture(t, sc)
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
	driveRate(t, f, "/premium/base", 60, time.Hour)
	driveRate(t, f, "/premium/high", 180, 15*time.Minute)
	driveRate(t, f, "/articles/base", 60, time.Hour)
	driveRate(t, f, "/articles/high", 180, 15*time.Minute)
	if p := priceOf(t, f, "/premium/still"); p != 20 {
		t.Fatalf("opted-out group moved: %d", p)
	}
	if p := priceOf(t, f, "/articles/now"); p < 7 {
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
	if !strings.Contains(string(raw), `"baseline_per_minute"`) || !strings.Contains(string(raw), `"warmed"`) {
		t.Fatalf("status missing baseline fields: %s", raw)
	}
}

func TestSurgeRateMath(t *testing.T) {
	s := newSurger(surgeOpts(true))
	now := time.Unix(1_800_000_000, 0)
	for i := 0; i < 30; i++ {
		s.hit("/premium/**", now, false)
	}
	s.mu.Lock()
	g := s.groups["/premium/**"]
	cfg, _ := s.cfg.forGroup("/premium/**")
	s.decayLocked(g, cfg, now)
	rate := s.rateLocked(g)
	s.mu.Unlock()
	want := 30 * surgeLn2 / surgeHalfLife.Minutes()
	if rate < want*0.99 || rate > want*1.01 {
		t.Fatalf("rate %g, want ~%g", rate, want)
	}
	if g.warmed {
		t.Fatal("30 instants should not finish the 1h warm-up")
	}
}

func TestSurgeWarmupSeedsBaseline(t *testing.T) {
	s := newSurger(surgeOpts(true))
	start := time.Unix(1_800_000_000, 0)
	// 60 events/min for one hour.
	for i := 0; i < 60; i++ {
		now := start.Add(time.Duration(i) * time.Minute)
		for j := 0; j < 60; j++ {
			s.hit("/premium/**", now.Add(time.Duration(j)*time.Second), false)
		}
	}
	now := start.Add(time.Hour)
	s.mu.Lock()
	g := s.groups["/premium/**"]
	cfg, _ := s.cfg.forGroup("/premium/**")
	s.decayLocked(g, cfg, now)
	s.maybeWarmLocked(g, cfg, now)
	base := s.baselineRateLocked(g, cfg)
	s.mu.Unlock()
	if !g.warmed {
		t.Fatal("should be warmed")
	}
	// 3600 hits / 60 minutes = 60/min seed.
	if base < 55 || base > 65 {
		t.Fatalf("seeded baseline %g, want ~60", base)
	}
}
