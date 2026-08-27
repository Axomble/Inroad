//go:build integration

package throttle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/ratelimit"
)

func redisAddr() string {
	if v := os.Getenv("INROAD_REDIS_ADDR"); v != "" {
		return v
	}
	return "localhost:6379"
}

// TestIntegrationThrottleOverCapRealRedis drives the middleware against a real
// Redis fixed-window limiter: the first IPLimit requests pass, the next is a 429
// with Retry-After. A unique bucket isolates the run so a shared Redis carries no
// cross-test state.
func TestIntegrationThrottleOverCapRealRedis(t *testing.T) {
	limiter := ratelimit.NewRedisLimiter(redisAddr())
	t.Cleanup(func() { _ = limiter.Close() })
	// Fail-closed by design means an unreachable Redis DENIES rather than passing,
	// which would make this test's "first N pass" precondition impossible — so skip
	// (not fail) when Redis is down, matching the ratelimit integration convention.
	if _, err := limiter.Allow(context.Background(), "throttle-it-ping:"+uuid.NewString(), 1, time.Minute); err != nil {
		t.Skipf("redis unreachable at %s: %v", redisAddr(), err)
	}

	const limit = 3
	mw := Config{
		Limiter: limiter, Resolver: httpx.NewClientIPResolver(nil), Window: time.Minute,
		IPLimit: limit, AcctLimit: 0,
	}.Middleware("it-" + uuid.NewString())
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	for i := 1; i <= limit; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/login", http.NoBody))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/login", http.NoBody))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over cap: got %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 missing Retry-After header")
	}
}
