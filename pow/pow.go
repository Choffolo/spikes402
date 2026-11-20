// Package pow implements the Spikes402 proof-of-work puzzle.
//
// The puzzle is the same idea as Bitcoin mining, in miniature: find a
// nonce such that
//
//	SHA256(SHA256(challenge + ":" + nonce))
//
// starts with at least `difficulty` zero bits, where nonce is written as
// a decimal number. Each extra bit doubles the expected work.
package pow

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/bits"
	"strconv"
)

// MaxDifficulty is the highest difficulty this package accepts. It keeps
// both servers and clients from being asked for absurd amounts of work.
const MaxDifficulty = 40

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

// Solve searches for a nonce that solves the puzzle. It checks ctx every
// few thousand attempts so it can be cancelled.
func Solve(ctx context.Context, challenge string, difficulty int) (uint64, error) {
	if difficulty < 0 || difficulty > MaxDifficulty {
		return 0, ErrTooHard
	}
	for nonce := uint64(0); ; nonce++ {
		if nonce%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		if LeadingZeroBits(Hash(challenge, nonce)) >= difficulty {
			return nonce, nil
		}
	}
}

// DifficultyForPrice returns the default difficulty for a price in sats:
// 16 bits plus one bit for every doubling of the price (1 sat = 17 bits,
// 10 sats = 20 bits, 1000 sats = 26 bits), capped at 32.
//
// This is a placeholder heuristic so that a higher price also means more
// work. It is NOT an economic equivalence: at real mining rates one sat
// is worth far more hashing than a CPU can do. Calibrating it is an open
// question of the project.
func DifficultyForPrice(priceSats int64) int {
	if priceSats < 1 {
		priceSats = 1
	}
	d := 16 + bits.Len64(uint64(priceSats))
	if d > 32 {
		d = 32
	}
	return d
}
