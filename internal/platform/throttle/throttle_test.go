package throttle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// runThroughPeek drives r through the throttle middleware (which internally peeks
// the body for an account key) with AcctLimit>0 so the peek path runs, and a
// handler that echoes back the body it received. It returns the recorder and the
// bytes the handler saw — asserting both that the peek never failed the request and
// that the body was restored for the handler.
func runThroughPeek(t *testing.T, r *http.Request) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	var seen []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("handler could not read restored body: %v", err)
		}
		seen = b
		w.WriteHeader(http.StatusOK)
	})
	h := cfg(newFakeLimiter(), 1000, 1000).Middleware("login")(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, seen
}

// TestBodyPeek_MalformedJSON asserts an unparseable body degrades peekEmail to ""
// (IP-only throttling) without failing the request, and the handler still receives
// the body intact.
func TestBodyPeek_MalformedJSON(t *testing.T) {
	const raw = `{"email": "broken`
	if got := peekEmail(httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(raw))); got != "" {
		t.Fatalf("peekEmail on malformed JSON: got %q, want \"\" (IP-only fallback)", got)
	}
	rec, seen := runThroughPeek(t, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("malformed body: got %d, want 200 (peek never fails the request)", rec.Code)
	}
	if string(seen) != raw {
		t.Fatalf("handler saw %q, want body restored intact %q", seen, raw)
	}
}

// TestBodyPeek_EmptyBody asserts a missing/empty body degrades peekEmail to "" and
// passes through to the handler (which sees an empty body) without erroring.
func TestBodyPeek_EmptyBody(t *testing.T) {
	if got := peekEmail(httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(""))); got != "" {
		t.Fatalf("peekEmail on empty body: got %q, want \"\"", got)
	}
	rec, seen := runThroughPeek(t, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("")))
	if rec.Code != http.StatusOK {
		t.Fatalf("empty body: got %d, want 200", rec.Code)
	}
	if len(seen) != 0 {
		t.Fatalf("handler saw %q, want an empty body", seen)
	}
}

// TestBodyPeek_OversizedBody asserts a body larger than maxBodyPeek degrades
// peekEmail to "" WITHOUT parsing a partial body (so it can't be an account-key
// bypass) and still passes through. The restored body is truncated at the peek cap:
// that is truncated-but-safe — a downstream JSON decode fails to a 400, never a
// throttle bypass or a 500.
func TestBodyPeek_OversizedBody(t *testing.T) {
	// Syntactically valid JSON carrying an email, but larger than the peek cap.
	pad := strings.Repeat("x", maxBodyPeek+1024)
	raw := `{"email":"big@example.test","pad":"` + pad + `"}`
	if len(raw) <= maxBodyPeek {
		t.Fatalf("test body %d bytes is not over the %d cap", len(raw), maxBodyPeek)
	}
	if got := peekEmail(httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(raw))); got != "" {
		t.Fatalf("peekEmail on oversized body: got %q, want \"\" (IP-only, no partial parse)", got)
	}
	rec, seen := runThroughPeek(t, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Fatalf("oversized body: got %d, want 200 (peek never fails the request)", rec.Code)
	}
	// The restore keeps only what the peek read (maxBodyPeek+1 sentinel bytes) —
	// truncated, but safe (decode-fails to 400 downstream, no bypass).
	if len(seen) != maxBodyPeek+1 {
		t.Fatalf("restored oversized body = %d bytes, want %d (truncated at peek cap)", len(seen), maxBodyPeek+1)
	}
}
