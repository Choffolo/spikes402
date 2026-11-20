package gate

import (
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/internal/botid"
)

// Action is what a policy does with a request of some class.
type Action string

// Policy actions.
const (
	ActionPay     Action = "pay"      // normal pay-or-PoW (the default)
	ActionPoWOnly Action = "pow-only" // no invoice, proof-of-work only
	ActionFree    Action = "free"     // let it through without paying
	ActionBlock   Action = "block"    // 403 Forbidden
)

// Policy says how to treat one class of client. Policies are optional
// and off by default: with none configured every client, verified bot or
// not, gets the same price and the same choice of paying or doing PoW.
type Policy struct {
	Action          Action  `json:"action"`
	PriceMultiplier float64 `json:"price_multiplier,omitempty"` // 0 means 1
}

// Validate checks a policy.
func (p Policy) Validate() error {
	switch p.Action {
	case ActionPay, ActionPoWOnly, ActionFree, ActionBlock:
	default:
		return fmt.Errorf("unknown action %q (use pay, pow-only, free or block)", p.Action)
	}
	if p.PriceMultiplier < 0 || p.PriceMultiplier > 1000 || math.IsNaN(p.PriceMultiplier) {
		return fmt.Errorf("price_multiplier must be between 0 and 1000")
	}
	return nil
}

var defaultPolicy = Policy{Action: ActionPay, PriceMultiplier: 1}

// policyFor picks the policy for a class: first by bot name or Web Bot
// Auth agent URL (only once the claim is verified, so an impostor never
// gets a named bot's treatment), then by kind ("unknown", "impostor",
// "verified", "verified-signed"), else the default.
func (g *Gate) policyFor(set *Settings, c botid.Class) Policy {
	if c.Name != "" && (c.Kind == botid.Verified || c.Kind == botid.Signed) {
		if p, ok := set.Policies[c.Name]; ok {
			return p
		}
	}
	if p, ok := set.Policies[string(c.Kind)]; ok {
		return p
	}
	return defaultPolicy
}

// price applies the policy's multiplier, never going below 1 sat.
func (p Policy) price(base int64) int64 {
	m := p.PriceMultiplier
	if m == 0 {
		m = 1
	}
	v := math.Round(float64(base) * m)
	if v > 1e12 {
		v = 1e12
	}
	return max(int64(v), 1)
}

// rateLog prints at most perMinute lines a minute and counts the rest,
// so that an attacker cannot flood the logs.
type rateLog struct {
	mu         sync.Mutex
	perMinute  int
	start      time.Time
	count      int
	suppressed int
}

func (l *rateLog) printf(now time.Time, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.start) >= time.Minute {
		if l.suppressed > 0 {
			log.Printf("spikes402: %d log messages suppressed in the last minute", l.suppressed)
		}
		l.start, l.count, l.suppressed = now, 0, 0
	}
	if l.count >= l.perMinute {
		l.suppressed++
		return
	}
	l.count++
	log.Printf(format, args...)
}

// sanitize makes an attacker-controlled string safe for a log line.
func sanitize(s string) string {
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}
