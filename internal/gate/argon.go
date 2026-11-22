package gate

import (
	"sync"
	"time"
)

// Default and hard cap for the total memory of concurrent Argon2id checks
// (argon2_max_memory_mib). A single check cannot exceed this budget.
const (
	DefaultArgonMaxMemoryMiB = 256
	MaxArgonMaxMemoryMiB     = 1024
	argonAcquireWait         = 50 * time.Millisecond
)

// argonBudget limits how much memory concurrent Argon2id verifications may
// use together. acquire waits briefly, then the caller should answer 503
// rather than allocate past the budget.
type argonBudget struct {
	mu    sync.Mutex
	used  int
	max   int
	avail chan struct{}
}

func newArgonBudget(maxMiB int) *argonBudget {
	if maxMiB <= 0 {
		maxMiB = DefaultArgonMaxMemoryMiB
	}
	return &argonBudget{max: maxMiB, avail: make(chan struct{}, 1)}
}

func (a *argonBudget) acquire(mib int, wait time.Duration) bool {
	if mib < 1 || mib > a.max {
		return false
	}
	deadline := time.Now().Add(wait)
	for {
		a.mu.Lock()
		if a.used+mib <= a.max {
			a.used += mib
			a.mu.Unlock()
			return true
		}
		a.mu.Unlock()
		if wait <= 0 || !time.Now().Before(deadline) {
			return false
		}
		remaining := time.Until(deadline)
		select {
		case <-a.avail:
		case <-time.After(remaining):
			return false
		}
	}
}

func (a *argonBudget) release(mib int) {
	a.mu.Lock()
	a.used -= mib
	if a.used < 0 {
		a.used = 0
	}
	a.mu.Unlock()
	select {
	case a.avail <- struct{}{}:
	default:
	}
}

func (a *argonBudget) tight() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.max > 0 && a.used >= int(float64(a.max)*surgePressureFrac)
}
