package persist

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	spentSnapName = "spent.snap"
	spentLogName  = "spent.log"
	maxLogBytes   = 4 << 20
	maxLogLines   = 8192
)

// SpentRecord is one spent-token entry as stored on disk. IDs are the
// same strings the gate uses ("pay:<hex>", "pow:<hex>"). No secrets.
type SpentRecord struct {
	ID       string `json:"id"`
	Expires  int64  `json:"exp"`
	First    int64  `json:"first"`
	Retries  int    `json:"retries,omitempty"`
	Released bool   `json:"rel,omitempty"`
}

// Spent is an append-only journal of spent tokens, compacted to a snapshot.
type Spent struct {
	dir      string
	key      []byte
	mu       sync.Mutex
	entries  map[string]SpentRecord
	logLines int
	logBytes int64
}

// OpenSpent prepares the journal in dir. Call Load before Persist.
func OpenSpent(dir string, key []byte) (*Spent, error) {
	if err := Prepare(dir); err != nil {
		return nil, err
	}
	return &Spent{dir: dir, key: append([]byte(nil), key...)}, nil
}

// Load reads the snapshot and log, drops expired and tampered records,
// and compacts. A missing store is a first run (empty). A corrupt snapshot,
// an unreadable file, or a log whose every record is tampered is ErrCorrupt.
func (s *Spent) Load(now time.Time) ([]SpentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[string]SpentRecord)
	s.logLines, s.logBytes = 0, 0

	snapPath := filepath.Join(s.dir, spentSnapName)
	logPath := filepath.Join(s.dir, spentLogName)

	snap, err := os.ReadFile(snapPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// first run, or a compact that had not written a snapshot yet
	case err != nil:
		return nil, fmt.Errorf("spent snapshot: %w", err)
	default:
		payload, err := Unseal(s.key, string(snap))
		if err != nil {
			return nil, fmt.Errorf("spent snapshot: %w", ErrCorrupt)
		}
		var recs []SpentRecord
		if err := json.Unmarshal(payload, &recs); err != nil {
			return nil, fmt.Errorf("spent snapshot: %w", ErrCorrupt)
		}
		for _, r := range recs {
			if r.ID != "" {
				s.entries[r.ID] = r
			}
		}
	}

	raw, err := os.ReadFile(logPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// ok
	case err != nil:
		return nil, fmt.Errorf("spent log: %w", err)
	default:
		tampered, lines := 0, 0
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lines++
			payload, err := Unseal(s.key, line)
			if err != nil {
				tampered++
				continue
			}
			var r SpentRecord
			if err := json.Unmarshal(payload, &r); err != nil || r.ID == "" {
				tampered++
				continue
			}
			s.entries[r.ID] = r
		}
		s.logLines = lines
		s.logBytes = int64(len(raw))
		if tampered > 0 {
			log.Printf("spikes402: discarded %d tampered or unreadable spent-token record(s)", tampered)
		}
		if len(snap) == 0 && lines > 0 && tampered == lines {
			return nil, fmt.Errorf("spent log: %w", ErrCorrupt)
		}
	}

	unix := now.Unix()
	out := make([]SpentRecord, 0, len(s.entries))
	for id, r := range s.entries {
		if unix > r.Expires {
			delete(s.entries, id)
			continue
		}
		out = append(out, r)
	}
	if err := s.compactLocked(); err != nil {
		return nil, err
	}
	return out, nil
}

// Persist appends a record (a use or a release) and fsyncs.
func (s *Spent) Persist(r SpentRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]SpentRecord)
	}
	if s.logBytes >= maxLogBytes || s.logLines >= maxLogLines {
		if err := s.compactLocked(); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return err
	}
	line := Seal(s.key, payload)
	if err := appendLine(filepath.Join(s.dir, spentLogName), line); err != nil {
		return err
	}
	s.entries[r.ID] = r
	s.logLines++
	s.logBytes += int64(len(line) + 1)
	return nil
}

func (s *Spent) compactLocked() error {
	recs := make([]SpentRecord, 0, len(s.entries))
	for _, r := range s.entries {
		recs = append(recs, r)
	}
	payload, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.dir, spentSnapName, []byte(Seal(s.key, payload)+"\n")); err != nil {
		return err
	}
	logPath := filepath.Join(s.dir, spentLogName)
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(logPath, 0o600); err != nil {
		return err
	}
	s.logLines, s.logBytes = 0, 0
	return syncDir(s.dir)
}
