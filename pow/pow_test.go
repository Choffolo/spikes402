package pow

import (
	"context"
	"testing"
	"time"
)

func TestSolveAndCheck(t *testing.T) {
	const challenge = "hello"
	for _, d := range []int{0, 1, 12} {
		nonce, err := Solve(context.Background(), challenge, d)
		if err != nil {
			t.Fatal(err)
		}
		if !Check(challenge, nonce, d) {
			t.Fatalf("difficulty %d: solution does not verify", d)
		}
	}
	if Hash("hello", 1) == Hash("other", 1) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Solve(ctx, "x", MaxDifficulty); err == nil {
		t.Fatal("expected cancellation")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation took too long")
	}
}

func TestDifficultyForPrice(t *testing.T) {
	cases := []struct {
		price         int64
		secondsPerSat float64
		want          int
	}{
		{1, 10, 24},      // 2e7 hashes  -> 2^24.25
		{10, 10, 28},     // 2e8 hashes  -> 2^27.6
		{100, 10, 31},    // 2e9 hashes  -> 2^30.9
		{1000, 10, 32},   // capped
		{1, 0.001, 16},   // raised to the minimum
		{1, 0, 16},       // degenerate input
		{1 << 40, 1, 32}, // huge price
		{3, 1, 23},       // 6e6 hashes -> 2^22.5 rounds to 23
	}
	for _, c := range cases {
		if got := DifficultyForPrice(c.price, c.secondsPerSat); got != c.want {
			t.Errorf("price %d at %g s/sat: got %d, want %d", c.price, c.secondsPerSat, got, c.want)
		}
	}
}

func TestExpectedSeconds(t *testing.T) {
	if got := ExpectedSeconds(21); got < 1.04 || got > 1.05 { // 2^21 / 2e6
		t.Fatalf("got %g", got)
	}
}
