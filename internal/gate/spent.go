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
//
// One exception, for payers: if the origin fails to answer (see release),
// the token can be used again, a few times and only for a short while.
type spentSet struct {
	mu        sync.Mutex
	ids       map[string]*spentEntry
	max       int
	inserts   int
	lastSweep time.Time
}

type spentEntry struct {
	expires  time.Time
	first    time.Time // when it was first used
	retries  int       // how often it was given back
	released bool      // given back and not used again yet
}

// A token whose request the origin could not answer can be retried at most
// maxRetries times, within retryWindow of its first use. Whatever happens,
// it is delivered at most once.
const (
	maxRetries  = 3
	retryWindow = 30 * time.Second
)

// maxSpent bounds the memory of the spent set (roughly 100 MB at most).
const maxSpent = 1_000_000

func newSpentSet() *spentSet {
	return &spentSet{ids: make(map[string]*spentEntry), max: maxSpent}
}

// errBusy is returned when the spent set is full of unexpired tokens.
var errBusy = errors.New("server busy, try again later")

// use marks id as spent. It returns errReplay if id was already spent,
// or errBusy if the set is full: refusing is safer than forgetting.
func (s *spentSet) use(id string, expires, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, seen := s.ids[id]; seen {
		if e.released && now.Before(e.first.Add(retryWindow)) {
			e.released = false
			return nil
		}
		return errReplay
	}
	s.inserts++
	// Forget entries that can no longer be replayed: every 1024 inserts,
	// or when full (at most once a second, so a full set stays cheap).
	if s.inserts%1024 == 0 || (len(s.ids) >= s.max && now.Sub(s.lastSweep) >= time.Second) {
		for k, e := range s.ids {
			if now.After(e.expires) {
				delete(s.ids, k)
			}
		}
		s.lastSweep = now
	}
	if len(s.ids) >= s.max {
		return errBusy
	}
	s.ids[id] = &spentEntry{expires: expires, first: now}
	return nil
}

// release gives a token back after the origin failed to answer, so the same
// request can be tried again. It does nothing once the retries or the
// window are used up. The caller only calls it when nothing was delivered.
func (s *spentSet) release(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.ids[id]
	if !ok || e.released || e.retries >= maxRetries || !now.Before(e.first.Add(retryWindow)) {
		return
	}
	e.retries++
	e.released = true
}

func (s *spentSet) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ids)
}

// has reports whether id was already spent (and not given back).
func (s *spentSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, seen := s.ids[id]
	return seen && !e.released
}
