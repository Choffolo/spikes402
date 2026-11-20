package pow

import (
	"context"
	"testing"
	"time"
)

func TestSolveAndCheck(t *testing.T) {
	const challenge = "hello"
	nonce, err := Solve(context.Background(), challenge, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !Check(challenge, nonce, 12) {
		t.Fatal("solution does not verify")
	}
	if Hash(challenge, nonce) == Hash("other", nonce) {
		t.Fatal("hash must depend on the challenge")
	}
}

func TestLeadingZeroBits(t *testing.T) {
	var h [32]byte
	if got := LeadingZeroBits(h); got != 256 {
		t.Fatalf("all zero: got %d", got)
	}
	h[0], h[1] = 0x00, 0x1f // 8 + 3 zero bits
	if got := LeadingZeroBits(h); got != 11 {
		t.Fatalf("got %d, want 11", got)
	}
}

func TestDifficultyBounds(t *testing.T) {
	if Check("x", 0, MaxDifficulty+1) || Check("x", 0, -1) {
		t.Fatal("out-of-range difficulty must be rejected")
	}
	if _, err := Solve(context.Background(), "x", MaxDifficulty+1); err != ErrTooHard {
		t.Fatalf("got %v", err)
	}
}

func TestSolveCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := Solve(ctx, "x", MaxDifficulty); err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestDifficultyForPrice(t *testing.T) {
	cases := map[int64]int{0: 17, 1: 17, 10: 20, 1000: 26, 1 << 40: 32}
	for price, want := range cases {
		if got := DifficultyForPrice(price); got != want {
			t.Errorf("price %d: got %d, want %d", price, got, want)
		}
	}
}
