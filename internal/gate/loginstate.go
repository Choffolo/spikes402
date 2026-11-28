package gate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/internal/persist"
)

// Failure counters for login routes (see login.go). Keys are keyed hashes
// (HMAC with the secret key) of an account, a network or both: no
// usernames, passwords or IP addresses are kept, in memory or on disk.
// Counts decay exponentially, so an old failure weighs less and less.

const (
	counterPair byte = 1 // this account from this network
	counterAcct byte = 2 // this account from anywhere
	counterNet  byte = 3 // this network, any account

	maxLoginCounters = 1_000_000
	loginStateFile   = "login-counters"
	// A counter below this is forgotten (about 4.3 half-lives after one failure).
	counterForget = 0.05
)

type counterKey [17]byte // kind + 16-byte hash

type counter struct {
	f float64 // decayed number of failures
	t int64   // unix seconds of the last update
}

func halfLife(kind byte) float64 {
	if kind == counterNet {
		return 15 * 60
	}
	return 30 * 60
}

func (c counter) at(kind byte, now int64) float64 {
	if now <= c.t {
		return c.f
	}
	return c.f * math.Exp2(-float64(now-c.t)/halfLife(kind))
}

// LoginState holds the login failure counters. It is safe for concurrent use.
type LoginState struct {
	mu        sync.Mutex
	m         map[counterKey]counter
	max       int
	dirty     bool
	minute    int64 // unix minute of cur
	cur, prev int   // site-wide failures this minute and the previous one
	lastSweep int64
	surge     [2]int // site-wide failures per minute for +1 and +2 levels
}

// DefaultLoginSurge is the default for login_surge_failures_per_minute.
var DefaultLoginSurge = [2]int{100, 1000}

// CheckLoginSurge validates surge thresholds: two failure counts per
// minute, the first at least 1, the second larger than the first.
func CheckLoginSurge(t [2]int) error {
	if t[0] < 1 || t[1] <= t[0] || t[1] > 100_000_000 {
		return errors.New(`"login_surge_failures_per_minute" must be two increasing counts, e.g. [100, 1000]`)
	}
	return nil
}

// NewLoginState returns empty counters.
func NewLoginState() *LoginState {
	return &LoginState{m: make(map[counterKey]counter), max: maxLoginCounters, surge: DefaultLoginSurge}
}

func loginKey(kind byte, parts ...string) counterKey {
	h := sha256.New()
	h.Write([]byte{kind})
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	var k counterKey
	k[0] = kind
	copy(k[1:], h.Sum(nil))
	return k
}

// levelLocked returns the level required now for acct (hex HMAC) from net.
// The account's own counter shows as one bit (see loginAcctShow).
func (s *LoginState) levelLocked(acct, net string, now int64) int {
	read := func(kind byte, k counterKey) int {
		if c, ok := s.m[k]; ok {
			return loginStep(c.at(kind, now))
		}
		return 0
	}
	pair := read(counterPair, loginKey(counterPair, acct, net))
	acc := 0
	if read(counterAcct, loginKey(counterAcct, acct)) >= loginAcctShow {
		acc = loginAcctCap
	}
	nt := min(read(counterNet, loginKey(counterNet, net)), loginNetCap)
	lvl := LoginBaseLevel + max(pair, acc, nt) + s.surgeLocked(now)
	return min(lvl, LoginMaxLevel)
}

// Level returns the level required now for acct from net.
func (s *LoginState) Level(acct, net string, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.levelLocked(acct, net, now.Unix())
}

// admit checks that a proof of level lvl is still enough and, if so,
// counts the attempt as a failure until the origin says otherwise (see
// succeeded). Counting before forwarding means attempts sent in parallel
// cannot share one level: each one raises the next.
func (s *LoginState) admit(acct, net string, lvl int, now time.Time) (need int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unix := now.Unix()
	need = s.levelLocked(acct, net, unix)
	if lvl < need {
		return need, false
	}
	s.sweepLocked(unix)
	s.addLocked(counterPair, loginKey(counterPair, acct, net), 1, unix)
	s.addLocked(counterAcct, loginKey(counterAcct, acct), 1, unix)
	s.addLocked(counterNet, loginKey(counterNet, net), 1, unix)
	return need, true
}

// succeeded undoes the provisional failure of an admitted attempt that
// logged in: the pair counter (this account from this network) starts
// again from zero; the account and network counters only lose that one
// attempt and decay, so a login wipes neither the failures of a network
// nor, visibly to others, those of the account.
func (s *LoginState) succeeded(acct, net string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unix := now.Unix()
	delete(s.m, loginKey(counterPair, acct, net))
	s.addLocked(counterAcct, loginKey(counterAcct, acct), -1, unix)
	s.addLocked(counterNet, loginKey(counterNet, net), -1, unix)
	s.dirty = true
}

// netFailed counts one failure for a network that sent a proof with a
// right pre-check but a wrong Argon2id nonce.
func (s *LoginState) netFailed(net string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unix := now.Unix()
	s.sweepLocked(unix)
	s.addLocked(counterNet, loginKey(counterNet, net), 1, unix)
}

// failed records an admitted attempt that did not log in (the counters
// already hold it) in the site-wide rate behind the surge levels.
func (s *LoginState) failed(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollLocked(now.Unix())
	s.cur++
}

func (s *LoginState) addLocked(kind byte, k counterKey, d float64, now int64) {
	c, ok := s.m[k]
	if !ok && d <= 0 {
		return
	}
	if !ok && len(s.m) >= s.max {
		s.evictLocked(now)
	}
	f := max(c.at(kind, now)+d, 0)
	if f < counterForget {
		delete(s.m, k)
	} else {
		s.m[k] = counter{f: f, t: now}
	}
	s.dirty = true
}

// sweepLocked forgets counters that decayed to nothing, at most once a minute.
func (s *LoginState) sweepLocked(now int64) {
	if now-s.lastSweep < 60 {
		return
	}
	s.lastSweep = now
	for k, c := range s.m {
		if c.at(k[0], now) < counterForget {
			delete(s.m, k)
			s.dirty = true
		}
	}
}

// evictLocked makes room in a full table by forgetting the tenth of the
// counters that weigh least now (fewest failures, decayed the most). The
// heaviest counters, those of an attack in progress, are kept; pushing one
// out would take as many heavier counters, each paid for with proofs at
// a rising level. Newcomers are then tracked from the base level.
func (s *LoginState) evictLocked(now int64) {
	vals := make([]float64, 0, len(s.m))
	for k, c := range s.m {
		vals = append(vals, c.at(k[0], now))
	}
	n := max(len(vals)/10, 1)
	limit, removed := nthSmallest(vals, n-1), 0
	for k, c := range s.m {
		if removed >= n {
			break
		}
		if c.at(k[0], now) <= limit {
			delete(s.m, k)
			removed++
		}
	}
	s.dirty = true
}

// nthSmallest returns the k-th smallest value of a (0-based), reordering
// a: quickselect with random pivots, O(len(a)) on average.
func nthSmallest(a []float64, k int) float64 {
	lo, hi := 0, len(a)-1
	for lo < hi {
		p := a[lo+rand.IntN(hi-lo+1)]
		i, j := lo, hi
		for i <= j {
			for a[i] < p {
				i++
			}
			for a[j] > p {
				j--
			}
			if i <= j {
				a[i], a[j] = a[j], a[i]
				i++
				j--
			}
		}
		switch {
		case k <= j:
			hi = j
		case k >= i:
			lo = i
		default:
			return a[k]
		}
	}
	return a[k]
}

func (s *LoginState) rollLocked(now int64) {
	m := now / 60
	switch {
	case m == s.minute:
	case m == s.minute+1:
		s.minute, s.prev, s.cur = m, s.cur, 0
	default:
		s.minute, s.prev, s.cur = m, 0, 0
	}
}

func (s *LoginState) surgeLocked(now int64) int {
	s.rollLocked(now)
	switch n := max(s.cur, s.prev); {
	case n >= s.surge[1]:
		return 2
	case n >= s.surge[0]:
		return 1
	}
	return 0
}

// loginStep maps a (decayed) failure count to extra levels.
func loginStep(f float64) int {
	n := int(math.Round(f))
	if n >= len(loginSteps) {
		n = len(loginSteps) - 1
	}
	return loginSteps[max(n, 0)]
}

// Snapshot on disk: one HMAC-sealed line (persist.Seal) of JSON.
type loginSnapshot struct {
	V int             `json:"v"`
	E [][]interface{} `json:"e"` // [key hex, failures, unix seconds]
}

// Save writes a sealed snapshot to dir if anything changed. The counters
// are copied under the lock; formatting and writing happen outside it.
func (s *LoginState) Save(dir string, key []byte) error {
	type entry struct {
		k counterKey
		c counter
	}
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	entries := make([]entry, 0, len(s.m))
	for k, c := range s.m {
		entries = append(entries, entry{k, c})
	}
	s.dirty = false
	s.mu.Unlock()
	snap := loginSnapshot{V: 1, E: make([][]interface{}, 0, len(entries))}
	for _, e := range entries {
		snap.E = append(snap.E, []interface{}{hex.EncodeToString(e.k[:]), e.c.f, e.c.t})
	}
	data, err := json.Marshal(snap)
	if err == nil {
		err = persist.WriteFile(dir, loginStateFile, []byte(persist.Seal(key, data)+"\n"))
	}
	if err != nil {
		s.mu.Lock()
		s.dirty = true
		s.mu.Unlock()
	}
	return err
}

// LoadLoginState reads the snapshot in dir. A missing file gives empty
// counters; a file that fails its seal is an error (persist.ErrCorrupt),
// so an attack in progress is never forgotten silently.
func LoadLoginState(dir string, key []byte) (*LoginState, error) {
	s := NewLoginState()
	data, err := os.ReadFile(filepath.Join(dir, loginStateFile))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	payload, err := persist.Unseal(key, string(data))
	if err != nil {
		return nil, persist.ErrCorrupt
	}
	var snap struct {
		V int                  `json:"v"`
		E [][3]json.RawMessage `json:"e"`
	}
	if err := json.Unmarshal(payload, &snap); err != nil || snap.V != 1 {
		return nil, persist.ErrCorrupt
	}
	for _, e := range snap.E {
		var kh string
		var c counter
		if json.Unmarshal(e[0], &kh) != nil || json.Unmarshal(e[1], &c.f) != nil || json.Unmarshal(e[2], &c.t) != nil {
			return nil, persist.ErrCorrupt
		}
		b, err := hex.DecodeString(kh)
		if err != nil || len(b) != len(counterKey{}) || len(s.m) >= s.max {
			return nil, persist.ErrCorrupt
		}
		var k counterKey
		copy(k[:], b)
		s.m[k] = c
	}
	return s, nil
}

// StartPersisting saves a snapshot every minute while ctx is live, and
// once more when it ends.
func (s *LoginState) StartPersisting(ctx context.Context, dir string, key []byte) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				if err := s.Save(dir, key); err != nil {
					log.Printf("login counters: saving: %v", err)
				}
				return
			case <-t.C:
				if err := s.Save(dir, key); err != nil {
					log.Printf("login counters: saving: %v", err)
				}
			}
		}
	}()
}
