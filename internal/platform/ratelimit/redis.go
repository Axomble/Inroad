// Package ratelimit provides an atomic Redis fixed-window request limiter.
//
// It is deliberately tiny and transport-only: callers decide the key, the cap,
// and the window, and act on the (allowed, error) result. The one policy this
// package owns is atomicity — the increment and the window-expiry are applied in
// a single server-side Lua evaluation so N concurrent callers can never each read
// a stale sub-cap count and all proceed.
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// incrWindow atomically increments the counter at KEYS[1] and, on the first hit
// of a fresh window, arms its expiry (ARGV[1] = window in milliseconds). It
// returns the post-increment count. Setting the TTL only when the counter is
// created (== 1) anchors a fixed window at the first request in it, so a steady
// stream can never keep pushing the expiry out.
var incrWindow = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return n
`)

// RedisLimiter is a fixed-window limiter backed by Redis.
type RedisLimiter struct {
	rdb *redis.Client
}

// NewRedisLimiter builds a limiter over a Redis client dialed at addr. It reuses
// the same address the queue transport connects to; the client pools connections
// internally.
func NewRedisLimiter(addr string) *RedisLimiter {
	return &RedisLimiter{rdb: redis.NewClient(&redis.Options{Addr: addr})}
}

// Allow reports whether one more request under key is permitted within the
// current window, given a cap of limit requests per window. A non-positive limit
// is treated as "no cap" (always allowed). A Redis error is returned to the
// caller UNCONSUMED — the caller is expected to FAIL CLOSED (deny) on error
// rather than fall open, so an outage cannot lift the cap.
func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	n, err := incrWindow.Run(ctx, l.rdb, []string{"ratelimit:" + key}, window.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("ratelimit: redis incr: %w", err)
	}
	return n <= int64(limit), nil
}

// Close releases the underlying Redis client.
func (l *RedisLimiter) Close() error { return l.rdb.Close() }
