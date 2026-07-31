//go:build integration

package ratelimit

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// redisAddr mirrors the apikey integration convention: honor the same
// INROAD_REDIS_ADDR the app reads, defaulting to the local dev instance that
// `make db-up` publishes.
func redisAddr() string {
	if v := os.Getenv("INROAD_REDIS_ADDR"); v != "" {
		return v
	}
	return "localhost:6379"
}

// newLimiter dials the real Redis and skips (not fails) if it is unreachable, so
// the suite degrades gracefully when the integration Redis is down.
func newLimiter(t *testing.T) *RedisLimiter {
	t.Helper()
	l := NewRedisLimiter(redisAddr())
	t.Cleanup(func() { _ = l.Close() })
	if err := l.rdb.Ping(context.Background()).Err(); err != nil {
		t.Skipf("redis unreachable at %s: %v", redisAddr(), err)
	}
	return l
}

// uniqueKey isolates each test on its own counter so a shared Redis carries no
// cross-test state.
func uniqueKey(t *testing.T) string {
	t.Helper()
	return "it:" + t.Name() + ":" + uuid.NewString()
}

// TestAllowFixedWindowCap proves the first `limit` calls are allowed and the
// (limit+1)th is denied within one window.
func TestAllowFixedWindowCap(t *testing.T) {
	l := newLimiter(t)
	ctx := context.Background()
	key := uniqueKey(t)
	const limit = 5

	for i := 1; i <= limit; i++ {
		allowed, err := l.Allow(ctx, key, limit, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("call %d: got (allowed=%v, err=%v), want allowed", i, allowed, err)
		}
	}
	allowed, err := l.Allow(ctx, key, limit, time.Minute)
	if err != nil {
		t.Fatalf("over-limit call errored: %v", err)
	}
	if allowed {
		t.Fatalf("call %d allowed, want denied (over cap)", limit+1)
	}
}

// TestTTLArmedOnFirstHitNotPushedOut proves the window TTL is set on the FIRST
// hit and later hits do NOT extend it (a fixed window, not a sliding one): the
// TTL only ticks DOWN. It then proves the counter resets once the window elapses.
func TestTTLArmedOnFirstHitNotPushedOut(t *testing.T) {
	l := newLimiter(t)
	ctx := context.Background()
	redisKey := "ratelimit:" + uniqueKey(t)
	key := redisKey[len("ratelimit:"):]
	const window = 2 * time.Second

	if _, err := l.Allow(ctx, key, 100, window); err != nil {
		t.Fatalf("first hit: %v", err)
	}
	first, err := l.rdb.PTTL(ctx, redisKey).Result()
	if err != nil {
		t.Fatalf("PTTL after first hit: %v", err)
	}
	if first <= 0 || first > window {
		t.Fatalf("TTL after first hit = %v, want (0, %v]", first, window)
	}

	time.Sleep(300 * time.Millisecond)
	if _, err := l.Allow(ctx, key, 100, window); err != nil {
		t.Fatalf("second hit: %v", err)
	}
	second, err := l.rdb.PTTL(ctx, redisKey).Result()
	if err != nil {
		t.Fatalf("PTTL after second hit: %v", err)
	}
	// A pushed-out (sliding) window would RESET the TTL back to ~window here. A
	// fixed window only decays, so the second reading must be strictly less.
	if second >= first {
		t.Fatalf("TTL not decaying: first=%v second=%v (window was pushed out)", first, second)
	}

	// After the window fully elapses the counter is gone, so the cap resets.
	time.Sleep(window)
	allowed, err := l.Allow(ctx, key, 1, window)
	if err != nil {
		t.Fatalf("post-window hit: %v", err)
	}
	if !allowed {
		t.Fatal("counter did not reset after the window elapsed")
	}
}

// TestAllowConcurrentNeverExceedsLimit proves the atomic INCR+PEXPIRE lets at most
// `limit` of N concurrent callers through — no two racing callers each read a
// stale sub-cap count and both proceed.
func TestAllowConcurrentNeverExceedsLimit(t *testing.T) {
	l := newLimiter(t)
	ctx := context.Background()
	key := uniqueKey(t)
	const (
		limit   = 10
		callers = 50
	)

	var allowed int64
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			ok, err := l.Allow(ctx, key, limit, time.Minute)
			if err != nil {
				return
			}
			if ok {
				atomic.AddInt64(&allowed, 1)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&allowed); got != limit {
		t.Fatalf("allowed %d of %d concurrent callers, want exactly %d", got, callers, limit)
	}
}
