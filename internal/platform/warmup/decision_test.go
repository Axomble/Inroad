package warmup

import (
	"strconv"
	"testing"
)

// TestReplyDecisionDeterministic proves the seeded decision is reproducible: the
// same (seed, rate) always yields the same verdict, so a retried tick that rebuilds
// the send reaches the identical reply-vs-new branch.
func TestReplyDecisionDeterministic(t *testing.T) {
	const seed = "mbox-a:mbox-b:2026-07-26:3"
	first := ReplyDecision(seed, 0.5)
	for i := 0; i < 100; i++ {
		if got := ReplyDecision(seed, 0.5); got != first {
			t.Fatalf("decision not deterministic: run %d gave %v, want %v", i, got, first)
		}
	}
}

// TestReplyDecisionBounds proves the rate clamps: rate<=0 is never a reply, rate>=1
// is always a reply, regardless of seed.
func TestReplyDecisionBounds(t *testing.T) {
	for _, seed := range []string{"a", "b", "c", "long-seed-value-42"} {
		if ReplyDecision(seed, 0) {
			t.Fatalf("rate 0 should never reply (seed %q)", seed)
		}
		if ReplyDecision(seed, -0.5) {
			t.Fatalf("negative rate should never reply (seed %q)", seed)
		}
		if !ReplyDecision(seed, 1) {
			t.Fatalf("rate 1 should always reply (seed %q)", seed)
		}
		if !ReplyDecision(seed, 1.5) {
			t.Fatalf("rate >1 should always reply (seed %q)", seed)
		}
	}
}

// TestReplyDecisionRateMonotoneSpread proves a higher reply rate yields at least as
// many replies across a spread of seeds — the decision tracks the configured
// probability rather than ignoring it. Asserts behavior (the rate matters), not the
// exact hash mapping.
func TestReplyDecisionRateMonotoneSpread(t *testing.T) {
	const n = 2000
	count := func(rate float64) int {
		replies := 0
		for i := 0; i < n; i++ {
			if ReplyDecision(seedN(i), rate) {
				replies++
			}
		}
		return replies
	}
	low, mid, high := count(0.1), count(0.5), count(0.9)
	if low > mid || mid > high {
		t.Fatalf("reply counts not monotone in rate: 0.1=%d 0.5=%d 0.9=%d", low, mid, high)
	}
	// Sanity: 0.5 should land roughly in the middle, not degenerate to 0 or n.
	if mid < n/4 || mid > 3*n/4 {
		t.Fatalf("rate 0.5 produced %d/%d replies, expected roughly half", mid, n)
	}
}

func seedN(i int) string {
	return "seed-" + strconv.Itoa(i)
}
