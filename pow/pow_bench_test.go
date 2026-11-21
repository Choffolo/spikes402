package pow

import (
	"bytes"
	"context"
	"testing"
)

// BenchmarkSHA256dAttempt is one sha256d hash (the solver's inner loop).
func BenchmarkSHA256dAttempt(b *testing.B) {
	p := Params{Algorithm: SHA256d, Difficulty: 8}
	challenge := string(bytes.Repeat([]byte("t"), 320)) // length of a typical signed token
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = p.Hash(challenge, uint64(i))
	}
}

// BenchmarkSHA256dSolve is a cheap solve, so the per-attempt loop dominates.
func BenchmarkSHA256dSolve(b *testing.B) {
	p := Params{Algorithm: SHA256d, Difficulty: 8}
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := p.Solve(ctx, string(bytes.Repeat([]byte("t"), 320))); err != nil {
			b.Fatal(err)
		}
	}
}
