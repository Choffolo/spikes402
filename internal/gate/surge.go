package gate

import (
	"fmt"
	"maps"
	"math"
	"strings"
	"sync"
	"time"
)

// Defaults and bounds for optional surge pricing. Surge is off until
// enabled in the config file. Memory is one group per protect or prices
// pattern, not per URL.
const (
	MaxSurgeMultiplier = 10.0

	DefaultSurgeMaxMultiplier      = 3.0
	DefaultSurgeThresholdPerMinute = 60.0
	DefaultSurgeStep               = 0.25
	DefaultSurgeStepIntervalMin    = 2.0

	// surgeHalfLife is how fast a burst of 402s is forgotten. A short
	// spike barely moves the measured rate; a few minutes of sustained
	// demand does.
	surgeHalfLife = 3 * time.Minute

	// surgePressureFrac is "near full" for pending invoices and the
	// Argon2id memory budget (global pressure can raise the multiplier
	// the same way high demand does, still one step per interval).
	surgePressureFrac = 0.9
)

var surgeLn2 = math.Ln2

// SurgeSettings are the knobs for one rule group, or the site-wide
// defaults (top-level "surge" in the config file).
type SurgeSettings struct {
	// Enabled is a pointer so a per-group override can turn a pattern
	// off ("enabled": false) or on without confusing "omitted" with off.
	Enabled             *bool   `json:"enabled,omitempty"`
	MaxMultiplier       float64 `json:"max_multiplier,omitempty"`
	ThresholdPerMinute  float64 `json:"threshold_per_minute,omitempty"`
	Step                float64 `json:"step,omitempty"`
	StepIntervalMinutes float64 `json:"step_interval_minutes,omitempty"`
}

func surgeOn(v *bool) bool { return v != nil && *v }

// SurgeEnabled is a helper for tests and config examples.
func SurgeEnabled(on bool) *bool { return &on }

// SurgeConfig is optional dynamic pricing, off by default. When enabled,
// demand and the multiplier are tracked per rule group (a prices pattern
// if one matches, otherwise the matching protect pattern). Groups may
// override the top-level settings; a group with enabled set to false is
// left at 1× even if the site-wide switch is on.
type SurgeConfig struct {
	SurgeSettings
	Groups map[string]SurgeSettings `json:"groups,omitempty"`
}

// Active reports whether any group can move its price.
func (c SurgeConfig) Active() bool {
	if surgeOn(c.Enabled) {
		return true
	}
	for _, g := range c.Groups {
		if surgeOn(g.Enabled) {
			return true
		}
	}
	return false
}

// Normalize fills defaults and checks ranges. A completely empty (off)
// config is valid and left alone.
func (c *SurgeConfig) Normalize() error {
	if c == nil {
		return nil
	}
	// Leave a completely empty block alone so "surge" omitted stays omitted.
	if !c.Active() && c.MaxMultiplier == 0 && c.ThresholdPerMinute == 0 && c.Step == 0 && c.StepIntervalMinutes == 0 && len(c.Groups) == 0 {
		return nil
	}
	if err := c.SurgeSettings.validate("surge"); err != nil {
		return err
	}
	c.SurgeSettings.fill()
	for pat, g := range c.Groups {
		if pat == "" {
			return fmt.Errorf("surge.groups: empty pattern")
		}
		if err := g.validate("surge.groups[" + pat + "]"); err != nil {
			return err
		}
		// Do not fill group zeros: omitted knobs inherit the site-wide values.
		c.Groups[pat] = g
	}
	return nil
}

func (s *SurgeSettings) fill() {
	if s.MaxMultiplier == 0 {
		s.MaxMultiplier = DefaultSurgeMaxMultiplier
	}
	if s.ThresholdPerMinute == 0 {
		s.ThresholdPerMinute = DefaultSurgeThresholdPerMinute
	}
	if s.Step == 0 {
		s.Step = DefaultSurgeStep
	}
	if s.StepIntervalMinutes == 0 {
		s.StepIntervalMinutes = DefaultSurgeStepIntervalMin
	}
}

func (s SurgeSettings) validate(what string) error {
	m, th, step, iv := s.MaxMultiplier, s.ThresholdPerMinute, s.Step, s.StepIntervalMinutes
	if m == 0 {
		m = DefaultSurgeMaxMultiplier
	}
	if th == 0 {
		th = DefaultSurgeThresholdPerMinute
	}
	if step == 0 {
		step = DefaultSurgeStep
	}
	if iv == 0 {
		iv = DefaultSurgeStepIntervalMin
	}
	if math.IsNaN(m) || m <= 1 || m > MaxSurgeMultiplier {
		return fmt.Errorf(`%s.max_multiplier must be greater than 1 and at most %g`, what, MaxSurgeMultiplier)
	}
	if math.IsNaN(th) || th <= 0 || th > 1e6 {
		return fmt.Errorf(`%s.threshold_per_minute must be above 0 and at most 1,000,000`, what)
	}
	if math.IsNaN(step) || step <= 0 || step > m-1 {
		return fmt.Errorf(`%s.step must be above 0 and at most max_multiplier-1`, what)
	}
	if math.IsNaN(iv) || iv < 0.1 || iv > 1440 {
		return fmt.Errorf(`%s.step_interval_minutes must be between 0.1 and 1440`, what)
	}
	return nil
}

// forGroup returns the effective settings for a rule group.
func (c SurgeConfig) forGroup(pattern string) (SurgeSettings, bool) {
	base := c.SurgeSettings
	base.fill()
	if g, ok := lookupGroup(c.Groups, pattern); ok {
		merged := overlay(base, g)
		return merged, surgeOn(merged.Enabled)
	}
	return base, surgeOn(c.Enabled)
}

func lookupGroup(groups map[string]SurgeSettings, pattern string) (SurgeSettings, bool) {
	if groups == nil {
		return SurgeSettings{}, false
	}
	if g, ok := groups[pattern]; ok {
		return g, true
	}
	lower := strings.ToLower(pattern)
	for k, g := range groups {
		if strings.ToLower(k) == lower {
			return g, true
		}
	}
	return SurgeSettings{}, false
}

// overlay copies non-zero knobs from over onto base. A group's Enabled,
// if set, wins (opt in or out); otherwise the site-wide switch is kept.
func overlay(base, over SurgeSettings) SurgeSettings {
	out := base
	if over.Enabled != nil {
		out.Enabled = over.Enabled
	}
	if over.MaxMultiplier != 0 {
		out.MaxMultiplier = over.MaxMultiplier
	}
	if over.ThresholdPerMinute != 0 {
		out.ThresholdPerMinute = over.ThresholdPerMinute
	}
	if over.Step != 0 {
		out.Step = over.Step
	}
	if over.StepIntervalMinutes != 0 {
		out.StepIntervalMinutes = over.StepIntervalMinutes
	}
	return out
}

func cloneSurge(c SurgeConfig) SurgeConfig {
	c.Groups = maps.Clone(c.Groups)
	return c
}

// SurgeGroupStatus is the live multiplier and price of one rule group,
// for the dashboard and admin JSON.
type SurgeGroupStatus struct {
	Group           string  `json:"group"`
	Enabled         bool    `json:"enabled"`
	BaseSats        int64   `json:"base_sats"`
	Multiplier      float64 `json:"multiplier"`
	PriceSats       int64   `json:"price_sats"`
	DemandPerMinute float64 `json:"demand_per_minute"`
	MaxMultiplier   float64 `json:"max_multiplier"`
}

type surgeGroup struct {
	weight   float64
	last     time.Time
	lastStep time.Time
	mult     float64
}

// surger tracks demand and multipliers per rule group.
type surger struct {
	mu     sync.Mutex
	cfg    SurgeConfig
	groups map[string]*surgeGroup
}

func newSurger(cfg SurgeConfig) *surger {
	return &surger{cfg: cloneSurge(cfg), groups: map[string]*surgeGroup{}}
}

func (s *surger) setConfig(cfg SurgeConfig, keep map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cloneSurge(cfg)
	for k := range s.groups {
		if _, ok := keep[k]; !ok {
			delete(s.groups, k)
		}
	}
}

func (s *surger) getLocked(name string) *surgeGroup {
	g, ok := s.groups[name]
	if !ok {
		g = &surgeGroup{mult: 1}
		s.groups[name] = g
	}
	return g
}

func (s *surger) hit(name string, now time.Time, pressure bool) {
	if s == nil || name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, on := s.cfg.forGroup(name)
	if !on {
		return
	}
	g := s.getLocked(name)
	s.decayLocked(g, now)
	g.weight++
	s.stepLocked(g, cfg, now, pressure)
}

func (s *surger) multiplier(name string, now time.Time, pressure bool) float64 {
	if s == nil || name == "" {
		return 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, on := s.cfg.forGroup(name)
	if !on {
		return 1
	}
	g := s.getLocked(name)
	s.decayLocked(g, now)
	s.stepLocked(g, cfg, now, pressure)
	return g.mult
}

func (s *surger) price(name string, base int64, now time.Time, pressure bool) int64 {
	return applySurge(base, s.multiplier(name, now, pressure))
}

func (s *surger) decayLocked(g *surgeGroup, now time.Time) {
	if g.last.IsZero() {
		g.last = now
		if g.lastStep.IsZero() {
			g.lastStep = now
		}
		return
	}
	dt := now.Sub(g.last).Seconds()
	if dt > 0 {
		g.weight *= math.Exp(-surgeLn2 * dt / surgeHalfLife.Seconds())
		g.last = now
	}
}

func (s *surger) rateLocked(g *surgeGroup) float64 {
	return g.weight * surgeLn2 / surgeHalfLife.Minutes()
}

func (s *surger) stepLocked(g *surgeGroup, cfg SurgeSettings, now time.Time, pressure bool) {
	if g.lastStep.IsZero() {
		g.lastStep = now
		return
	}
	interval := time.Duration(cfg.StepIntervalMinutes * float64(time.Minute))
	if now.Sub(g.lastStep) < interval {
		return
	}
	rate := s.rateLocked(g)
	busy := rate >= cfg.ThresholdPerMinute || pressure
	switch {
	case busy && g.mult < cfg.MaxMultiplier:
		g.mult = min(cfg.MaxMultiplier, g.mult+cfg.Step)
		g.lastStep = now
	case !busy && g.mult > 1:
		g.mult = max(1, g.mult-cfg.Step)
		g.lastStep = now
	}
}

// applySurge multiplies a base price and rounds up to a whole sat.
func applySurge(base int64, m float64) int64 {
	if base < 1 {
		base = 1
	}
	if m <= 1 || math.IsNaN(m) {
		return base
	}
	v := math.Ceil(float64(base)*m - 1e-12)
	if v > 1e12 {
		v = 1e12
	}
	out := int64(v)
	if out < base {
		return base
	}
	return out
}

// sampleSurgePath is one path that pattern matches, used for status prices
// and to compare groups.
func sampleSurgePath(pattern string) string {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		if prefix == "" {
			return "/x"
		}
		return prefix + "/x"
	}
	return strings.ReplaceAll(pattern, "*", "x")
}

func surgePatterns(protect []string, prices []PriceRule) map[string]struct{} {
	keep := make(map[string]struct{}, len(protect)+len(prices))
	for _, p := range protect {
		keep[p] = struct{}{}
	}
	for _, r := range prices {
		keep[r.Pattern] = struct{}{}
	}
	return keep
}

// groupOf is the rule group for a request path: the first matching prices
// pattern, or the first matching protect pattern.
func (g *Gate) groupOf(p string, set *Settings) string {
	key := matchKey(p, g.opt.CaseSensitive)
	for _, r := range set.Prices {
		pat := r.Pattern
		if !g.opt.CaseSensitive {
			pat = strings.ToLower(pat)
		}
		if matchPattern(pat, key) {
			return r.Pattern
		}
	}
	for _, raw := range set.Protect {
		pat := raw
		if !g.opt.CaseSensitive {
			pat = strings.ToLower(pat)
		}
		if matchPattern(pat, key) {
			return raw
		}
	}
	return ""
}

func (g *Gate) offerPrice(p string, set *Settings) int64 {
	base := g.pathPrice(p, set)
	if g.surge == nil || !set.Surge.Active() {
		return base
	}
	return g.surge.price(g.groupOf(p, set), base, g.now(), g.underPressure())
}

func (g *Gate) underPressure() bool {
	if g.node != nil && g.node.status(g.now()).BreakerOpen {
		return true
	}
	if g.pending != nil && g.pending.nearCap() {
		return true
	}
	if g.argon != nil && g.argon.tight() {
		return true
	}
	return false
}

func (s *surger) snapshot(names []string, baseOf func(string) int64, now time.Time, pressure bool) []SurgeGroupStatus {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SurgeGroupStatus, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		cfg, on := s.cfg.forGroup(name)
		if !on && !s.cfg.Active() {
			continue
		}
		g := s.getLocked(name)
		s.decayLocked(g, now)
		if on {
			s.stepLocked(g, cfg, now, pressure)
		}
		mult := 1.0
		if on {
			mult = g.mult
		}
		base := baseOf(name)
		out = append(out, SurgeGroupStatus{
			Group: name, Enabled: on, BaseSats: base, Multiplier: mult,
			PriceSats: applySurge(base, mult), DemandPerMinute: s.rateLocked(g),
			MaxMultiplier: cfg.MaxMultiplier,
		})
	}
	return out
}

func (g *Gate) surgeStatus(set *Settings, now time.Time, pressure bool) []SurgeGroupStatus {
	if g.surge == nil || set == nil {
		return nil
	}
	names := make([]string, 0, len(set.Prices)+len(set.Protect))
	for _, r := range set.Prices {
		names = append(names, r.Pattern)
	}
	for _, p := range set.Protect {
		names = append(names, p)
	}
	return g.surge.snapshot(names, func(pattern string) int64 {
		if r, ok := priceRule(set.Prices, pattern); ok {
			return r
		}
		return PriceFor(sampleSurgePath(pattern), set.Prices, set.PriceSats, g.opt.CaseSensitive)
	}, now, pressure)
}

func priceRule(prices []PriceRule, pattern string) (int64, bool) {
	for _, r := range prices {
		if r.Pattern == pattern {
			return r.PriceSats, true
		}
	}
	return 0, false
}
