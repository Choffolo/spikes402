package admin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/internal/config"
)

const (
	maxProposals   = 100
	proposalMaxAge = 24 * time.Hour
)

// Proposal is a suggested settings change waiting for the human admin.
type Proposal struct {
	ID      string         `json:"id"`
	Created time.Time      `json:"created"`
	By      Role           `json:"by"`
	Reason  string         `json:"reason,omitempty"`
	Changes config.Changes `json:"changes"`
	Status  string         `json:"status"` // pending, applied, rejected, failed, expired
	Result  string         `json:"result,omitempty"`
}

type proposals struct {
	mu    sync.Mutex
	items []*Proposal // oldest first, at most maxProposals
}

func newProposals() *proposals { return &proposals{} }

func (p *proposals) expireLocked(now time.Time) {
	for _, x := range p.items {
		if x.Status == "pending" && now.Sub(x.Created) > proposalMaxAge {
			x.Status = "expired"
		}
	}
}

func (p *proposals) add(ch config.Changes, reason string, by Role, now time.Time) (Proposal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(now)
	if len(p.items) >= maxProposals {
		// Drop the oldest decided proposal; refuse if all are pending.
		i := 0
		for i < len(p.items) && p.items[i].Status == "pending" {
			i++
		}
		if i == len(p.items) {
			return Proposal{}, errors.New("too many pending proposals")
		}
		p.items = append(p.items[:i], p.items[i+1:]...)
	}
	id := make([]byte, 8)
	rand.Read(id)
	x := &Proposal{ID: hex.EncodeToString(id), Created: now.UTC(), By: by, Reason: reason, Changes: ch, Status: "pending"}
	p.items = append(p.items, x)
	return *x, nil
}

func (p *proposals) list(now time.Time) []Proposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(now)
	out := make([]Proposal, 0, len(p.items))
	for i := len(p.items) - 1; i >= 0; i-- { // newest first
		out = append(out, *p.items[i])
	}
	return out
}

func (p *proposals) pending(now time.Time) int {
	n := 0
	for _, x := range p.list(now) {
		if x.Status == "pending" {
			n++
		}
	}
	return n
}

// take marks a pending proposal as being decided so it is used only once.
func (p *proposals) take(id string, now time.Time) (Proposal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(now)
	for _, x := range p.items {
		if x.ID == id {
			if x.Status != "pending" {
				return Proposal{}, errors.New("proposal is " + x.Status)
			}
			x.Status = "deciding"
			return *x, nil
		}
	}
	return Proposal{}, errors.New("no such proposal")
}

func (p *proposals) finish(id, status, result string) Proposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, x := range p.items {
		if x.ID == id {
			x.Status, x.Result = status, truncate(result, 300)
			return *x
		}
	}
	return Proposal{}
}
