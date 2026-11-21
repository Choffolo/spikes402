package config

import (
	"fmt"
	"strings"

	"github.com/Choffolo/spikes402/internal/gate"
)

// WeakensProtection lists ways next is easier on visitors than cur.
// An empty list means protection is stricter or equal.
func WeakensProtection(cur, next *Config) []string {
	if cur == nil || next == nil {
		return nil
	}
	var why []string
	if next.Paused && !cur.Paused {
		why = append(why, "pause would serve protected paths free")
	}
	if next.PriceSats < cur.PriceSats {
		why = append(why, fmt.Sprintf("price_sats would fall from %d to %d", cur.PriceSats, next.PriceSats))
	}
	if next.PowSecondsPerSat < cur.PowSecondsPerSat {
		why = append(why, "pow_seconds_per_sat would be lowered")
	}
	if next.ChallengesPerMinute > cur.ChallengesPerMinute {
		why = append(why, "challenges_per_minute would be raised")
	}
	if dcur, dnext := cur.PoW(cur.PriceSats).Difficulty, next.PoW(next.PriceSats).Difficulty; dnext < dcur {
		why = append(why, fmt.Sprintf("proof-of-work difficulty would fall from %d to %d", dcur, dnext))
	}

	curPat := map[string]struct{}{}
	for _, p := range cur.Protect {
		curPat[p] = struct{}{}
	}
	nextPat := map[string]struct{}{}
	for _, p := range next.Protect {
		nextPat[p] = struct{}{}
	}
	for p := range curPat {
		if _, ok := nextPat[p]; !ok {
			why = append(why, "a protect pattern would be removed or narrowed ("+p+")")
			break
		}
	}

	caseSens := cur.ProtectCaseSensitive || next.ProtectCaseSensitive
	for _, r := range cur.Prices {
		old := r.PriceSats
		newP := gate.PriceFor(samplePath(r.Pattern), next.Prices, next.PriceSats, caseSens)
		if newP < old {
			why = append(why, fmt.Sprintf("price for %s would fall from %d to %d", r.Pattern, old, newP))
		}
	}
	seen := map[string]struct{}{}
	for _, r := range cur.Prices {
		seen[r.Pattern] = struct{}{}
	}
	for _, r := range next.Prices {
		if _, ok := seen[r.Pattern]; ok {
			continue
		}
		was := gate.PriceFor(samplePath(r.Pattern), cur.Prices, cur.PriceSats, caseSens)
		if r.PriceSats < was {
			why = append(why, fmt.Sprintf("new price rule for %s (%d sats) is below the current %d", r.Pattern, r.PriceSats, was))
		}
	}

	keys := map[string]struct{}{}
	for k := range cur.BotPolicies {
		keys[k] = struct{}{}
	}
	for k := range next.BotPolicies {
		keys[k] = struct{}{}
	}
	for k := range keys {
		a, b := cur.BotPolicies[k], next.BotPolicies[k]
		if policyRank(b) < policyRank(a) {
			why = append(why, fmt.Sprintf("bot policy %s would be weaker (%s → %s)", k, policyAction(a), policyAction(b)))
		}
		if policyMultiplier(b) < policyMultiplier(a) {
			why = append(why, fmt.Sprintf("bot policy %s price_multiplier would be lowered", k))
		}
	}
	return why
}

func policyAction(p gate.Policy) string {
	if p.Action == "" {
		return string(gate.ActionPay)
	}
	return string(p.Action)
}

func policyRank(p gate.Policy) int {
	switch p.Action {
	case gate.ActionFree:
		return 0
	case gate.ActionBlock:
		return 2
	default:
		return 1 // pay, pow-only, or empty (default pay)
	}
}

func policyMultiplier(p gate.Policy) float64 {
	if p.PriceMultiplier == 0 {
		return 1
	}
	return p.PriceMultiplier
}

// samplePath is one path that pattern matches, used to compare prices.
func samplePath(pattern string) string {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		if prefix == "" {
			return "/x"
		}
		return prefix + "/x"
	}
	return strings.ReplaceAll(pattern, "*", "x")
}
