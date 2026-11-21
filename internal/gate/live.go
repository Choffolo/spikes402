package gate

import (
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/Choffolo/spikes402/internal/botid"
	"github.com/Choffolo/spikes402/pow"
)

// Settings are the options an admin may change while the gate runs.
// Changes are swapped in atomically and apply to new 402 offers.
type Settings struct {
	PriceSats           int64
	Prices              []PriceRule // first match wins; empty means use PriceSats only
	PoW                 func(priceSats int64) pow.Params
	Policies            map[string]Policy
	ChallengesPerMinute int
}

// Settings returns the settings in force. Do not modify the Policies map.
func (g *Gate) Settings() Settings { return *g.live.Load() }

// UpdateSettings replaces the live settings. Validate user input with the
// config package first; this only rejects values that would break the gate.
func (g *Gate) UpdateSettings(s Settings) error {
	if s.PriceSats < 1 || s.PoW == nil || s.ChallengesPerMinute < 1 {
		return errors.New("gate: invalid settings")
	}
	for _, p := range s.Policies {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	g.limiter.setPerMinute(s.ChallengesPerMinute)
	s.Prices = append([]PriceRule(nil), s.Prices...)
	g.live.Store(&s)
	return nil
}

// Outcome is what happened to one request to a protected path.
type Outcome string

// Outcomes, from the visitor's point of view.
const (
	OutcomePaid        Outcome = "paid"         // valid Lightning payment proof
	OutcomePoW         Outcome = "pow-solved"   // valid proof-of-work
	OutcomeChallenged  Outcome = "challenged"   // got a 402 offer
	OutcomeRejected    Outcome = "rejected"     // sent a wrong, expired or reused proof (got a new 402)
	OutcomeRateLimited Outcome = "rate-limited" // 429
	OutcomeBusy        Outcome = "busy"         // 503, proof-of-work checks full
	OutcomeBlocked     Outcome = "blocked"      // 403 by a bot policy
	OutcomeFree        Outcome = "free"         // let through by a bot policy
)

// Event describes one request to a protected path, for monitoring. It
// holds the raw client address: observers must anonymise it before
// storing or showing it.
type Event struct {
	Time     time.Time
	Client   string // rate-limit key: IPv4 address or IPv6 /64
	IP       net.IP
	Class    botid.Class
	Resource string
	Outcome  Outcome
}

func (g *Gate) observe(r *http.Request, class botid.Class, o Outcome) {
	if g.opt.Observer == nil {
		return
	}
	g.opt.Observer(Event{Time: g.now(), Client: g.client(r), IP: g.clientIP(r), Class: class, Resource: resource(r), Outcome: o})
}

// Status is a snapshot of the gate's internal state for the dashboard.
type Status struct {
	OpenInvoices int        `json:"open_invoices"`
	SpentTokens  int        `json:"spent_tokens"`
	Node         NodeStatus `json:"node"`
}

// NodeStatus describes the guard in front of the Lightning node.
type NodeStatus struct {
	BreakerOpen         bool      `json:"breaker_open"`
	BreakerOpenUntil    time.Time `json:"breaker_open_until,omitzero"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	InFlight            int       `json:"in_flight"`
	InvoiceBudget       float64   `json:"invoice_budget"` // invoices that may be created right now
	InvoicesCreated     uint64    `json:"invoices_created"`
	InvoiceFailures     uint64    `json:"invoice_failures"`
	LastError           string    `json:"last_error,omitempty"`
	LastErrorAt         time.Time `json:"last_error_at,omitzero"`
}

// Status returns a snapshot of the gate's state.
func (g *Gate) Status() Status {
	return Status{OpenInvoices: g.pending.len(), SpentTokens: g.spent.len(), Node: g.node.status(g.now())}
}
