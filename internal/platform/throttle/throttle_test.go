package throttle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// fakeLimiter counts hits per key and allows up to the per-call limit; an injected
// err makes every Allow fail (to exercise the fail-closed path).
type fakeLimiter struct {
	mu     sync.Mutex
	counts map[string]int
	err    error
}

func newFakeLimiter() *fakeLimiter { return &fakeLimiter{counts: map[string]int{}} }

func (f *fakeLimiter) Allow(_ context.Context, key string, limit int, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	f.counts[key]++
	return f.counts[key] <= limit, nil
}

func cfg(l Limiter, ipLimit, acctLimit int) Config {
	return Config{
		Limiter: l, Resolver: httpx.NewClientIPResolver(nil), Window: time.Minute,
		IPLimit: ipLimit, AcctLimit: acctLimit,
	}
}

func req(email string) *http.Request {
	var r *http.Request
	if email == "" {
		r = httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
	} else {
		buf, _ := json.Marshal(map[string]string{"email": email})
		r = httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(buf))
	}
	return r
}

func okNext() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// TestIPThrottleOverCapReturns429 asserts requests over the per-IP cap get a 429
// with a Retry-After header (never a pass, never a 500).
func TestIPThrottleOverCapReturns429(t *testing.T) {
	mw := cfg(newFakeLimiter(), 2, 0).Middleware("login")
	h := mw(okNext())

	for i := 1; i <= 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req(""))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over cap: got %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "60" {
		t.Fatalf("Retry-After: got %q, want \"60\"", ra)
	}
}

// TestAccountThrottleKeysByEmail asserts the per-account cap trips independently of
// IP: two requests carrying the same email trip it, a different email does not.
func TestAccountThrottleKeysByEmail(t *testing.T) {
	mw := cfg(newFakeLimiter(), 1000, 1).Middleware("login")
	h := mw(okNext())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("a@example.test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("first a@: got %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req("a@example.test"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second a@ over account cap: got %d, want 429", rec.Code)
	}
	// A different account is unaffected (independent key).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req("b@example.test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("first b@: got %d, want 200", rec.Code)
	}
}

// TestEmailCaseInsensitive asserts the account key is case-folded so "A@x" and
// "a@x" share one bucket.
func TestEmailCaseInsensitive(t *testing.T) {
	mw := cfg(newFakeLimiter(), 1000, 1).Middleware("login")
	h := mw(okNext())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("User@Example.test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("first: got %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req("user@example.test"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("case variant over cap: got %d, want 429", rec.Code)
	}
}

// TestFailClosedOnLimiterError asserts a limiter error (e.g. Redis down) denies
// with 429 rather than passing or 500ing.
func TestFailClosedOnLimiterError(t *testing.T) {
	l := newFakeLimiter()
	l.err = errors.New("redis: connection refused")
	h := cfg(l, 10, 0).Middleware("login")(okNext())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(""))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("limiter error: got %d, want 429 (fail closed)", rec.Code)
	}
}

// TestBodyRestoredForHandler asserts the account-keying peek restores the body so
// the downstream handler still decodes it intact.
func TestBodyRestoredForHandler(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		seen = body.Email
		w.WriteHeader(http.StatusOK)
	})
	h := cfg(newFakeLimiter(), 1000, 1000).Middleware("login")(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req("keep@example.test"))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if seen != "keep@example.test" {
		t.Fatalf("handler saw email %q, want body restored intact", seen)
	}
}
