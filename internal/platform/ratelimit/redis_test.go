package ratelimit

import (
	"context"
	"testing"
	"time"
)

// TestAllowNonPositiveLimitAlwaysAllows proves a non-positive cap is treated as
// "no limit" and never dials Redis (the guard returns before any I/O), so it is
// safe to unit-test without a server.
func TestAllowNonPositiveLimitAlwaysAllows(t *testing.T) {
	// Point at an unreachable address: if the guard were to dial, this would error.
	l := NewRedisLimiter("127.0.0.1:1")
	t.Cleanup(func() { _ = l.Close() })

	for _, limit := range []int{0, -1, -1000} {
		allowed, err := l.Allow(context.Background(), "k", limit, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("limit %d: got (allowed=%v, err=%v), want (true, nil)", limit, allowed, err)
		}
	}
}

// TestAllowFailsClosedOnUnreachableRedis proves an infra outage surfaces as an
// error (and allowed=false), so the verifier that consults this limiter FAILS
// CLOSED — an unreachable Redis denies rather than lifting the cap.
func TestAllowFailsClosedOnUnreachableRedis(t *testing.T) {
	l := NewRedisLimiter("127.0.0.1:1") // nothing listening -> connection refused
	t.Cleanup(func() { _ = l.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	allowed, err := l.Allow(ctx, "k", 5, time.Minute)
	if err == nil {
		t.Fatal("Allow returned nil error against an unreachable Redis (would fail OPEN)")
	}
	if allowed {
		t.Fatal("Allow returned allowed=true on infra error (must fail closed)")
	}
}
