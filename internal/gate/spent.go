package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Choffolo/spikes402/internal/persist"
)

// spentSet remembers redeemed token IDs until they expire, so that each
// token can be used only once. When a SpentStore is set, a use is written
// to disk before use returns, so a restart cannot replay a spent token.
// The id is reserved in memory first and the (slow) disk write happens
// without holding mu, so a slow disk never blocks other lookups; if the
// write fails the reservation is undone and the token is not spent.
//
// One exception, for payers: if the origin fails to answer (see release),
// the token can be used again, a few times and only for a short while.
//
// Keys are compact: 1 byte kind + 16-byte id. Expiry is stored as Unix
// seconds. The public methods still take the same string ids as before.
// SpentStore persists spent tokens across restarts.
type SpentStore interface {
	Persist(persist.SpentRecord) error
}

type spentSet struct {
	mu        sync.Mutex
	ids       map[spentID]spentEntry
	max       int
	logins    int // entries of kind spentKindLogin
	maxLogin  int // at most this many of them
	junk      int // login entries with a wrong nonce (retries > 0)
	maxJunk   int // at most this many of them
	inserts   int
	lastSweep time.Time
	store     SpentStore
}

// spentID is "pay", "pow" or "login" plus the 16-byte token id.
type spentID [17]byte

type spentEntry struct {
	expires  int64 // Unix seconds
	first    int64 // Unix seconds of first use
	retries  int   // how often it was given back
	released bool  // given back and not used again yet
}

const (
	spentKindPay   byte = 1
	spentKindPoW   byte = 2
	spentKindLogin byte = 3
	spentKindOther byte = 0 // tests and unexpected ids
)

// A token whose request the origin could not answer can be retried at most
// maxRetries times, within retryWindow of its first use. Whatever happens,
// it is delivered at most once.
const (
	maxRetries  = 3
	retryWindow = 30 * time.Second
)

// maxSpent bounds the memory of the spent set (about 128 MiB, ~134 bytes
// per entry, measured with HeapAlloc at the cap).
const maxSpent = 1_000_000

// maxLoginSpent is the share of maxSpent login tokens may use, so a
// flood of logins cannot make payers' tokens errBusy. Entries of right
// proofs are bounded by the attacker's work (a long-lived token is an
// expensive one), junk ones by maxLoginJunk.
const maxLoginSpent = 250_000

// maxLoginJunk caps the login entries of tokens checked with a wrong nonce
// (see wrongLogin). They live until their token expires, up to hours, so
// past the cap one of them is forgotten: that can only give a junk token
// another check (still rate-limited), never replay a right proof.
const maxLoginJunk = 100_000

func newSpentSet() *spentSet {
	return &spentSet{ids: make(map[spentID]spentEntry), max: maxSpent, maxLogin: maxLoginSpent, maxJunk: maxLoginJunk}
}

func (s *spentSet) load(recs []persist.SpentRecord) {
	for _, r := range recs {
		k := compactID(r.ID)
		s.del(k)
		if k[0] == spentKindLogin {
			s.logins++
			if r.Retries > 0 {
				s.junk++
			}
		}
		s.ids[k] = spentEntry{expires: r.Expires, first: r.First, retries: r.Retries, released: r.Released}
	}
}

// del forgets one entry. s.mu must be held.
func (s *spentSet) del(k spentID) {
	if e, ok := s.ids[k]; ok && k[0] == spentKindLogin {
		s.logins--
		if e.retries > 0 {
			s.junk--
		}
	}
	delete(s.ids, k)
}

func toSpentRecord(id string, e spentEntry) persist.SpentRecord {
	return persist.SpentRecord{ID: id, Expires: e.expires, First: e.first, Retries: e.retries, Released: e.released}
}

// compactID packs a spent-set key. Production keys are "pay:" or "pow:"
// followed by a 32-character hex id (16 bytes). Anything else is hashed,
// so existing tests that use short names keep working.
func compactID(id string) spentID {
	var k spentID
	kind, rest, ok := strings.Cut(id, ":")
	if ok && len(rest) == 32 {
		if raw, err := hex.DecodeString(rest); err == nil && len(raw) == 16 {
			switch kind {
			case "pay":
				k[0] = spentKindPay
			case "pow":
				k[0] = spentKindPoW
			case "login":
				k[0] = spentKindLogin
			default:
				k[0] = spentKindOther
			}
			if k[0] != spentKindOther {
				copy(k[1:], raw)
				return k
			}
		}
	}
	k[0] = spentKindOther
	sum := sha256.Sum256([]byte(id))
	copy(k[1:], sum[:16])
	return k
}

// errBusy is returned when the spent set is full of unexpired tokens.
var errBusy = errors.New("server busy, try again later")

// use marks id as spent. It returns errReplay if id was already spent,
// or errBusy if the set (or, for a login token, the login quota) is full:
// refusing is safer than forgetting.
func (s *spentSet) use(id string, expires, now time.Time) error {
	s.mu.Lock()
	key := compactID(id)
	if e, seen := s.ids[key]; seen {
		if e.released && key[0] != spentKindLogin && now.Unix() < e.first+int64(retryWindow.Seconds()) {
			prev := e
			e.released = false
			s.ids[key] = e // reserve
			s.mu.Unlock()
			if err := s.persist(id, e); err != nil {
				s.mu.Lock()
				s.ids[key] = prev
				s.mu.Unlock()
				return err
			}
			return nil
		}
		s.mu.Unlock()
		return errReplay
	}
	s.inserts++
	// Forget entries that can no longer be replayed: every 1024 inserts,
	// or when full (at most once a second, so a full set stays cheap).
	login := key[0] == spentKindLogin
	full := func() bool { return len(s.ids) >= s.max || (login && s.logins >= s.maxLogin) }
	if s.inserts%1024 == 0 || (full() && now.Sub(s.lastSweep) >= time.Second) {
		unix := now.Unix()
		for k, e := range s.ids {
			if unix > e.expires {
				s.del(k)
			}
		}
		s.lastSweep = now
	}
	if full() {
		s.mu.Unlock()
		return errBusy
	}
	e := spentEntry{expires: expires.Unix(), first: now.Unix()}
	s.ids[key] = e // reserve: a concurrent use of the same id is a replay
	if login {
		s.logins++
	}
	s.mu.Unlock()
	if err := s.persist(id, e); err != nil {
		s.mu.Lock()
		s.del(key)
		s.mu.Unlock()
		return err
	}
	return nil
}

// beginLogin reserves a login token for one Argon2id check (see
// checkLoginWork); the entry lives until the token expires. A token whose
// first check had a wrong nonce may be checked once more, no sooner than
// retryAfter later; any other token already seen is a replay.
func (s *spentSet) beginLogin(id string, expires, now time.Time, retryAfter time.Duration) error {
	s.mu.Lock()
	key := compactID(id)
	e, seen := s.ids[key]
	if !seen {
		s.mu.Unlock()
		return s.use(id, expires, now)
	}
	if !e.released || e.retries != 1 || now.Unix() < e.first+int64(retryAfter/time.Second) {
		s.mu.Unlock()
		return errReplay
	}
	prev := e
	e.released = false
	s.ids[key] = e // reserve: a concurrent retry is a replay
	s.mu.Unlock()
	if err := s.persist(id, e); err != nil {
		s.mu.Lock()
		s.ids[key] = prev
		s.mu.Unlock()
		return err
	}
	return nil
}

// wrongLogin records a wrong nonce for a token reserved by beginLogin.
// After the first, the token may be checked once more (released, retries
// 1); after the second it stays spent until it expires. Past maxJunk such
// entries, another one is forgotten (see maxLoginJunk).
func (s *spentSet) wrongLogin(id string, now time.Time) {
	s.mu.Lock()
	key := compactID(id)
	e, ok := s.ids[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	if e.retries == 0 {
		if s.junk >= s.maxJunk {
			for k, o := range s.ids {
				// Never a retry in progress (retries 1, not released):
				// forgetting it would let its right proof be replayed.
				if k[0] == spentKindLogin && (o.retries > 1 || o.retries == 1 && o.released) && k != key {
					s.del(k)
					break
				}
			}
		}
		s.junk++
	}
	e.retries++
	e.released = e.retries == 1
	e.first = now.Unix()
	s.ids[key] = e
	s.mu.Unlock()
	_ = s.persist(id, e) // if the disk fails, memory still holds it
}

// rightLogin records a right nonce for a token reserved by beginLogin.
// After a wrong one it is no longer junk, so it is never forgotten early;
// and if its entry is missing anyway, it is spent again until expires.
func (s *spentSet) rightLogin(id string, expires, now time.Time) {
	s.mu.Lock()
	key := compactID(id)
	e, ok := s.ids[key]
	switch {
	case !ok:
		e = spentEntry{expires: expires.Unix(), first: now.Unix()}
		s.logins++
	case e.retries == 0:
		s.mu.Unlock()
		return
	default:
		e.retries = 0
		s.junk--
	}
	s.ids[key] = e
	s.mu.Unlock()
	_ = s.persist(id, e)
}

// persist writes a record to disk. It must be called without s.mu held.
func (s *spentSet) persist(id string, e spentEntry) error {
	if s.store == nil {
		return nil
	}
	if err := s.store.Persist(toSpentRecord(id, e)); err != nil {
		return errBusy
	}
	return nil
}

// release gives a token back after the origin failed to answer, so the same
// request can be tried again. It does nothing once the retries or the
// window are used up. The caller only calls it when nothing was delivered.
func (s *spentSet) release(id string, now time.Time) {
	s.mu.Lock()
	key := compactID(id)
	e, ok := s.ids[key]
	if !ok || e.released || e.retries >= maxRetries || now.Unix() >= e.first+int64(retryWindow.Seconds()) {
		s.mu.Unlock()
		return
	}
	e.retries++
	e.released = true
	s.ids[key] = e
	s.mu.Unlock()
	_ = s.persist(id, e) // if disk fails, still release in this process
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
	e, seen := s.ids[compactID(id)]
	return seen && !e.released
}
