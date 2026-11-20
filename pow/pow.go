// Package pow implements the Spikes402 proof-of-work puzzle.
//
// The puzzle is the same idea as Bitcoin mining, in miniature: find a
// nonce such that
//
//	SHA256(SHA256(challenge + ":" + nonce))
//
// starts with at least `difficulty` zero bits, where nonce is written as
// a decimal number. Each extra bit doubles the expected work: on average
// 2^difficulty attempts are needed.
package pow

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"math/bits"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

// MaxDifficulty is the highest difficulty this package accepts. It keeps
// both servers and clients from being asked for absurd amounts of work.
const MaxDifficulty = 40

// Calibration. See "Proof-of-work pricing" in the README.
const (
	// ReferenceHashrate is how many puzzle attempts per second one
	// commodity CPU core does (measured: ~2.7 million/s for a Spikes402
	// challenge on a recent x86 server core with SHA extensions, rounded
	// down). A gaming GPU is roughly 1000 times faster.
	ReferenceHashrate = 2_000_000

	// Bounds for the difficulty derived from a price.
	MinAutoDifficulty = 16 // ~0.03 s on one core: below this the puzzle is pointless
	MaxAutoDifficulty = 32 // ~36 min on one core: above this no CPU bot would bother
)

// Hash returns SHA256(SHA256(challenge + ":" + nonce)).
func Hash(challenge string, nonce uint64) [32]byte {
	buf := make([]byte, 0, len(challenge)+21)
	buf = append(buf, challenge...)
	buf = append(buf, ':')
	buf = strconv.AppendUint(buf, nonce, 10)
	first := sha256.Sum256(buf)
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

// Check reports whether nonce solves the puzzle for challenge at the
// given difficulty.
func Check(challenge string, nonce uint64, difficulty int) bool {
	if difficulty < 0 || difficulty > MaxDifficulty {
		return false
	}
	return LeadingZeroBits(Hash(challenge, nonce)) >= difficulty
}

// ErrTooHard is returned by Solve when the difficulty is out of range.
var ErrTooHard = errors.New("pow: difficulty out of range")

// Solve searches for a nonce that solves the puzzle, using all CPU cores.
// It stops early if ctx is cancelled.
func Solve(ctx context.Context, challenge string, difficulty int) (uint64, error) {
	if difficulty < 0 || difficulty > MaxDifficulty {
		return 0, ErrTooHard
	}
	workers := uint64(runtime.NumCPU())
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
			for i := 0; ; i++ {
				if i%4096 == 0 && (found.Load() || ctx.Err() != nil) {
					return
				}
				if LeadingZeroBits(Hash(challenge, nonce)) >= difficulty {
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

// DifficultyForPrice converts a price into a puzzle difficulty, so that
// solving takes about secondsPerSat seconds of one reference CPU core per
// sat: difficulty = round(log2(price × secondsPerSat × ReferenceHashrate)),
// clamped to [MinAutoDifficulty, MaxAutoDifficulty].
func DifficultyForPrice(priceSats int64, secondsPerSat float64) int {
	hashes := float64(priceSats) * secondsPerSat * ReferenceHashrate
	if hashes < 1 {
		return MinAutoDifficulty
	}
	d := int(math.Round(math.Log2(hashes)))
	return min(max(d, MinAutoDifficulty), MaxAutoDifficulty)
}

// ExpectedSeconds is the average time one reference CPU core needs to
// solve a puzzle of the given difficulty.
func ExpectedSeconds(difficulty int) float64 {
	return math.Ldexp(1, difficulty) / ReferenceHashrate
}
