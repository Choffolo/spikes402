package gate

import (
	"errors"
	"sync"
	"time"
)

// spentSet remembers redeemed token IDs until they expire, so that each
// token can be used only once. It lives in memory: restarting the proxy
// forgets it, which is safe only because tokens expire quickly anyway
// (see "Known limits" in the README).
type spentSet struct {
	mu        sync.Mutex
	ids       map[string]time.Time // token ID -> expiry
	max       int
	inserts   int
	lastSweep time.Time
}

// maxSpent bounds the memory of the spent set (roughly 100 MB at most).
const maxSpent = 1_000_000

func newSpentSet() *spentSet {
	return &spentSet{ids: make(map[string]time.Time), max: maxSpent}
}

// errBusy is returned when the spent set is full of unexpired tokens.
var errBusy = errors.New("server busy, try again later")

// use marks id as spent. It returns errReplay if id was already spent,
// or errBusy if the set is full: refusing is safer than forgetting.
func (s *spentSet) use(id string, expires, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.ids[id]; seen {
		return errReplay
	}
	s.inserts++
	// Forget entries that can no longer be replayed: every 1024 inserts,
	// or when full (at most once a second, so a full set stays cheap).
	if s.inserts%1024 == 0 || (len(s.ids) >= s.max && now.Sub(s.lastSweep) >= time.Second) {
		for k, exp := range s.ids {
			if now.After(exp) {
				delete(s.ids, k)
			}
		}
		s.lastSweep = now
	}
	if len(s.ids) >= s.max {
		return errBusy
	}
	s.ids[id] = expires
	return nil
}

// has reports whether id was already spent.
func (s *spentSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, seen := s.ids[id]
	return seen
}
