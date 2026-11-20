// Package pow implements the Spikes402 proof-of-work puzzles.
//
// Find a nonce such that H(challenge + ":" + nonce) starts with at least
// `difficulty` zero bits, where nonce is written as a decimal number and
// H is one of:
//
//   - "sha256d": SHA256(SHA256(x)), like Bitcoin mining in miniature.
//     Cheap to check, but GPUs are about 1000 times faster than a CPU.
//   - "argon2id": Argon2id(x, salt "spikes402-pow-v1", 1 pass, M MiB,
//     1 lane, 32 bytes). Memory-hard, so a GPU or ASIC gains much less over
//     an ordinary CPU; each attempt (and each check) costs M MiB and some
//     milliseconds.
//
// Each extra bit doubles the expected work: 2^difficulty attempts on average.
package pow

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"golang.org/x/crypto/argon2"
)

// Algorithms.
const (
	SHA256d  = "sha256d"
	Argon2id = "argon2id"
)

// Bounds accepted by both servers and clients.
const (
	MaxDifficulty      = 40 // sha256d
	MaxArgonDifficulty = 24
	MinArgonMemoryMiB  = 8
	MaxArgonMemoryMiB  = 256
	argonSalt          = "spikes402-pow-v1"
)

// Calibration. See "Proof-of-work pricing" in the README.
const (
	// ReferenceHashrate is how many sha256d attempts per second one
	// commodity CPU core does (measured ~2.7 million/s on a recent server
	// core with SHA extensions, rounded down).
	ReferenceHashrate = 2_000_000

	// ReferenceArgonMiBps is how many MiB of Argon2id (1 pass) one core
	// processes per second (measured ~1,900 MiB/s at 16-64 MiB on the
	// same core, rounded down): one 16 MiB attempt takes ~11 ms.
	ReferenceArgonMiBps = 1500

	// Bounds for the difficulty derived from a price.
	MinAutoDifficulty = 16 // sha256d: ~0.03 s on one core
	MaxAutoDifficulty = 32 // sha256d: ~36 min on one core
	MinAutoArgon      = 1
	MaxAutoArgon      = 20
)

// Params describe one puzzle.
type Params struct {
	Algorithm  string
	Difficulty int
	MemoryMiB  int // argon2id only
}

// Validate checks the parameters are within the accepted bounds.
func (p Params) Validate() error {
	switch p.Algorithm {
	case SHA256d:
		if p.Difficulty < 0 || p.Difficulty > MaxDifficulty {
			return ErrTooHard
		}
	case Argon2id:
		if p.Difficulty < 0 || p.Difficulty > MaxArgonDifficulty ||
			p.MemoryMiB < MinArgonMemoryMiB || p.MemoryMiB > MaxArgonMemoryMiB {
			return ErrTooHard
		}
	default:
		return fmt.Errorf("pow: unknown algorithm %q", p.Algorithm)
	}
	return nil
}

// ErrTooHard is returned for parameters out of range.
var ErrTooHard = errors.New("pow: parameters out of range")

func input(challenge string, nonce uint64) []byte {
	buf := make([]byte, 0, len(challenge)+21)
	buf = append(buf, challenge...)
	buf = append(buf, ':')
	return strconv.AppendUint(buf, nonce, 10)
}

// Hash computes the puzzle hash. p must be valid.
func (p Params) Hash(challenge string, nonce uint64) [32]byte {
	in := input(challenge, nonce)
	if p.Algorithm == Argon2id {
		var out [32]byte
		copy(out[:], argon2.IDKey(in, []byte(argonSalt), 1, uint32(p.MemoryMiB)*1024, 1, 32))
		return out
	}
	first := sha256.Sum256(in)
	return sha256.Sum256(first[:])
}

// LeadingZeroBits counts the zero bits at the start of h.
func LeadingZeroBits(h [32]byte) int {
	n := 0
	for _, b := range h {
		if b != 0 {
			return n + bits.LeadingZeros8(b)
		}
		n += 8
	}
	return n
}

// Check reports whether nonce solves the puzzle. Invalid params never do.
func (p Params) Check(challenge string, nonce uint64) bool {
	return p.Validate() == nil && LeadingZeroBits(p.Hash(challenge, nonce)) >= p.Difficulty
}

// Solve searches for a nonce, in parallel. For argon2id the number of
// workers is capped at 4 to bound memory (4 x MemoryMiB).
func (p Params) Solve(ctx context.Context, challenge string) (uint64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	workers := uint64(runtime.NumCPU())
	check := uint64(4096)
	if p.Algorithm == Argon2id {
		workers, check = min(workers, 4), 1
	}
	var (
		found  atomic.Bool
		result uint64
		once   sync.Once
		wg     sync.WaitGroup
	)
	for w := uint64(0); w < workers; w++ {
		wg.Add(1)
		go func(nonce uint64) { // worker w tries w, w+workers, w+2*workers, ...
			defer wg.Done()
			for i := uint64(0); ; i++ {
				if i%check == 0 && (found.Load() || ctx.Err() != nil) {
					return
				}
				if LeadingZeroBits(p.Hash(challenge, nonce)) >= p.Difficulty {
					once.Do(func() { result = nonce; found.Store(true) })
					return
				}
				nonce += workers
			}
		}(w)
	}
	wg.Wait()
	if !found.Load() {
		return 0, ctx.Err()
	}
	return result, nil
}

// attemptSeconds is the time of one attempt on one reference core.
func attemptSeconds(algorithm string, memoryMiB int) float64 {
	if algorithm == Argon2id {
		return float64(memoryMiB) / ReferenceArgonMiBps
	}
	return 1.0 / ReferenceHashrate
}

// ForPrice returns puzzle parameters whose expected solve time on one
// reference CPU core is about priceSats x secondsPerSat:
// difficulty = round(log2(price x secondsPerSat / seconds per attempt)),
// clamped (sha256d: 16-32 bits, argon2id: 1-20 bits).
func ForPrice(algorithm string, memoryMiB int, priceSats int64, secondsPerSat float64) Params {
	lo, hi := MinAutoDifficulty, MaxAutoDifficulty
	if algorithm == Argon2id {
		lo, hi = MinAutoArgon, MaxAutoArgon
	}
	attempts := float64(priceSats) * secondsPerSat / attemptSeconds(algorithm, memoryMiB)
	d := lo
	if attempts >= 1 {
		d = min(max(int(math.Round(math.Log2(attempts))), lo), hi)
	}
	return Params{Algorithm: algorithm, Difficulty: d, MemoryMiB: memoryMiB}
}

// ExpectedSeconds is the average time one reference CPU core needs.
func (p Params) ExpectedSeconds() float64 {
	return math.Ldexp(1, p.Difficulty) * attemptSeconds(p.Algorithm, p.MemoryMiB)
}
