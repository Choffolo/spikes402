package gate

import (
	"sync"
	"time"
)

// spentSet remembers redeemed token IDs until they expire, so that each
// token can be used only once. It lives in memory: restarting the proxy
// forgets it, which is safe only because tokens expire quickly anyway
// (see "Known limits" in the README).
type spentSet struct {
	mu      sync.Mutex
	ids     map[string]time.Time // token ID -> expiry
	inserts int
}

func newSpentSet() *spentSet {
	return &spentSet{ids: make(map[string]time.Time)}
}

// use marks id as spent. It returns false if id was already spent.
func (s *spentSet) use(id string, expires, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.ids[id]; seen {
		return false
	}
	s.ids[id] = expires
	s.inserts++
	if s.inserts%1024 == 0 { // forget entries that can no longer be replayed
		for k, exp := range s.ids {
			if now.After(exp) {
				delete(s.ids, k)
			}
		}
	}
	return true
}

// has reports whether id was already spent.
func (s *spentSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, seen := s.ids[id]
	return seen
}
