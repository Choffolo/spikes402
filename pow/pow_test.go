package pow

import (
	"context"
	"testing"
	"time"
)

var sha = Params{Algorithm: SHA256d, Difficulty: 12}
var argon = Params{Algorithm: Argon2id, Difficulty: 2, MemoryMiB: 8}

func TestSolveAndCheck(t *testing.T) {
	for _, p := range []Params{sha, {Algorithm: SHA256d}, argon} {
		nonce, err := p.Solve(context.Background(), "hello")
		if err != nil {
			t.Fatal(err)
		}
		if !p.Check("hello", nonce) {
			t.Fatalf("%+v: solution does not verify", p)
		}
	}
	if sha.Hash("hello", 1) == sha.Hash("other", 1) || argon.Hash("hello", 1) == sha.Hash("hello", 1) {
		t.Fatal("hash must depend on challenge and algorithm")
	}
}

func TestArgonMemoryMatters(t *testing.T) {
	a8, a16 := argon, argon
	a16.MemoryMiB = 16
	if a8.Hash("x", 1) == a16.Hash("x", 1) {
		t.Fatal("memory parameter must change the hash")
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

func TestValidate(t *testing.T) {
	bad := []Params{
		{Algorithm: SHA256d, Difficulty: MaxDifficulty + 1},
		{Algorithm: SHA256d, Difficulty: -1},
		{Algorithm: Argon2id, Difficulty: MaxArgonDifficulty + 1, MemoryMiB: 16},
		{Algorithm: Argon2id, Difficulty: 1, MemoryMiB: 4},    // too little memory
		{Algorithm: Argon2id, Difficulty: 1, MemoryMiB: 1024}, // memory bomb
		{Algorithm: "md5", Difficulty: 1},
	}
	for _, p := range bad {
		if p.Validate() == nil || p.Check("x", 0) {
			t.Errorf("%+v accepted", p)
		}
		if _, err := p.Solve(context.Background(), "x"); err == nil {
			t.Errorf("%+v: solve accepted", p)
		}
	}
}

func TestSolveCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	for _, p := range []Params{{Algorithm: SHA256d, Difficulty: MaxDifficulty}, {Algorithm: Argon2id, Difficulty: MaxArgonDifficulty, MemoryMiB: 8}} {
		if _, err := p.Solve(ctx, "x"); err == nil {
			t.Fatal("expected cancellation")
		}
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation took too long")
	}
}

func TestForPrice(t *testing.T) {
	cases := []struct {
		alg           string
		mem           int
		price         int64
		secondsPerSat float64
		want          int
	}{
		{SHA256d, 0, 1, 10, 24},    // 2e7 attempts -> 2^24.25
		{SHA256d, 0, 2, 5, 24},     // same work as 1 sat at 10 s/sat
		{SHA256d, 0, 10, 10, 28},   // 2e8 -> 2^27.6
		{SHA256d, 0, 100, 10, 31},  // 2e9 -> 2^30.9
		{SHA256d, 0, 1000, 10, 32}, // capped
		{SHA256d, 0, 1, 0.001, 16}, // raised to the minimum
		{SHA256d, 0, 1, 0, 16},     // degenerate
		{SHA256d, 0, 3, 1, 23},     // 6e6 -> 2^22.5
		{Argon2id, 16, 1, 10, 10},  // 10 s / (16/1500 s) = 937 -> 2^9.9
		{Argon2id, 64, 1, 10, 8},   // 10 s / (64/1500 s) = 234 -> 2^7.9
		{Argon2id, 16, 10, 10, 13}, // 9375 -> 2^13.2
		{Argon2id, 16, 1 << 30, 10, 20},
		{Argon2id, 16, 1, 0.001, 1},
	}
	for _, c := range cases {
		if got := ForPrice(c.alg, c.mem, c.price, c.secondsPerSat); got.Difficulty != c.want || got.Validate() != nil {
			t.Errorf("%s %d MiB, %d sats at %g s/sat: got %+v, want %d", c.alg, c.mem, c.price, c.secondsPerSat, got, c.want)
		}
	}
}

func TestExpectedSeconds(t *testing.T) {
	if got := (Params{Algorithm: SHA256d, Difficulty: 21}).ExpectedSeconds(); got < 1.04 || got > 1.05 { // 2^21 / 2e6
		t.Fatalf("got %g", got)
	}
	if got := (Params{Algorithm: Argon2id, Difficulty: 10, MemoryMiB: 16}).ExpectedSeconds(); got < 10.9 || got > 11.0 { // 1024 x 16/1500
		t.Fatalf("got %g", got)
	}
}

func TestTwoSatsAtFiveSecondsMatchesOneSatAtTen(t *testing.T) {
	two := ForPrice(SHA256d, 0, 2, 5)
	one := ForPrice(SHA256d, 0, 1, 10)
	if two != one || two.Difficulty != 24 {
		t.Fatalf("got two=%+v one=%+v", two, one)
	}
	got, want := two.ExpectedSeconds(), (Params{Algorithm: SHA256d, Difficulty: 24}).ExpectedSeconds()
	if got != want || got < 8.3 || got > 8.4 { // 2^24 / 2e6 = 8.388608
		t.Fatalf("expected seconds: got %g, want %g", got, want)
	}
	if ForPrice(Argon2id, 16, 2, 5) != ForPrice(Argon2id, 16, 1, 10) {
		t.Fatal("argon2id work should also match")
	}
}
