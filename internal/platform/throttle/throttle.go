// Package throttle is the pre-authentication HTTP rate-limit middleware guarding
// abusable unauthenticated endpoints (login, 2FA/passkey verify, password-forgot,
// email-OTP). It throttles each request on TWO independent keys — the client IP
// and, where the body carries one, the target account (email) — so neither a
// single source address nor a single victim account can be hammered.
//
// It is fail-closed: the underlying limiter denies on a backing-store outage, and
// this middleware surfaces that as a clean 429 (never a 500, never a silent pass),
// so a Redis outage cannot lift the cap.
package throttle

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// maxBodyPeek bounds how much of a request body the account-keying path reads
// into memory before restoring it for the handler. Pre-auth bodies are tiny
// (an email + a code); a larger body simply isn't inspected for an account key
// (it still gets IP-throttled and passes through intact up to this cap).
const maxBodyPeek = 64 << 10 // 64 KiB

// Limiter is the fixed-window limiter seam (satisfied by ratelimit.RedisLimiter).
// Allow reports whether one more request under key is permitted; a non-nil error
// means the caller must fail closed (deny).
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// Config parameterises a throttle. One Config produces per-endpoint middleware via
// Middleware(bucket): the bucket namespaces the Redis keys so endpoints keep
// independent windows. AcctLimit <= 0 disables per-account keying (for endpoints
// whose body carries no email, e.g. 2FA/passkey verify) — those are IP-throttled
// only.
type Config struct {
	Limiter   Limiter
	Resolver  httpx.ClientIPResolver
	Window    time.Duration
	IPLimit   int
	AcctLimit int
}

// Middleware returns rate-limit middleware for one endpoint, keyed under bucket.
// Every request is checked against the per-IP cap; requests whose JSON body
// carries a non-empty "email" are ALSO checked against the per-account cap. Over
// either cap → 429 with Retry-After; a limiter error → 429 (fail closed).
func (c Config) Middleware(bucket string) func(http.Handler) http.Handler {
	retryAfter := strconv.Itoa(int(c.Window.Seconds()))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			ip := "unknown"
			if addr := c.Resolver.ClientIP(r); addr.IsValid() {
				ip = addr.String()
			}
			if !c.check(ctx, bucket+":ip:"+ip, c.IPLimit) {
				deny(w, retryAfter)
				return
			}

			if c.AcctLimit > 0 {
				if email := peekEmail(r); email != "" {
					if !c.check(ctx, bucket+":acct:"+email, c.AcctLimit) {
						deny(w, retryAfter)
						return
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// check reports whether the request is allowed under key. It fails CLOSED: a
// limiter error (e.g. Redis unreachable) is treated as "not allowed" so an outage
// cannot lift the cap.
func (c Config) check(ctx context.Context, key string, limit int) bool {
	allowed, err := c.Limiter.Allow(ctx, key, limit, c.Window)
	if err != nil {
		return false
	}
	return allowed
}

// deny writes the 429 with a Retry-After hint.
func deny(w http.ResponseWriter, retryAfter string) {
	w.Header().Set("Retry-After", retryAfter)
	httpx.Error(w, http.StatusTooManyRequests, "too many requests")
}

// peekEmail reads the request's JSON body, extracts a lower-cased "email" field,
// and RESTORES the body so the downstream handler decodes it normally. A body
// larger than maxBodyPeek, malformed JSON, or a missing email yields "" (the
// request is then IP-throttled only) — the read is best-effort and never fails the
// request itself.
func peekEmail(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxBodyPeek+1))
	if err != nil {
		return ""
	}
	// Restore the body for the handler regardless of what we parse out of it. For
	// an oversized body this restores only what we read (truncated to the peek cap):
	// pre-auth bodies are tiny, so a truncated one simply decode-fails to a 400
	// downstream — never a throttle bypass.
	r.Body = io.NopCloser(bytes.NewReader(buf))
	if len(buf) > maxBodyPeek {
		return "" // oversized: don't parse a partial body, IP-throttle only
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(buf, &body); err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(body.Email))
}
