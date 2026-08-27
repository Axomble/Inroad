package captcha

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/platform/httpx"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubClient builds an *http.Client whose transport runs fn — so a test drives
// siteverify without any network.
func stubClient(fn roundTripFunc) *http.Client { return &http.Client{Transport: fn} }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// TestNoopAlwaysPasses: an UNCONFIGURED deployment accepts every request, even a
// blank token.
func TestNoopAlwaysPasses(t *testing.T) {
	if err := NewNoop().Verify(context.Background(), "", ""); err != nil {
		t.Fatalf("noop rejected: %v", err)
	}
}

// TestConfiguredValidPasses: success=true → pass. Also asserts the secret and the
// client token are submitted to siteverify.
func TestConfiguredValidPasses(t *testing.T) {
	var gotSecret, gotResponse, gotIP string
	v := NewTurnstile("s3cr3t", stubClient(func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		gotSecret = r.PostForm.Get("secret")
		gotResponse = r.PostForm.Get("response")
		gotIP = r.PostForm.Get("remoteip")
		return jsonResponse(http.StatusOK, `{"success":true}`), nil
	}))
	if err := v.Verify(context.Background(), "tok-123", "203.0.113.7"); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if gotSecret != "s3cr3t" || gotResponse != "tok-123" || gotIP != "203.0.113.7" {
		t.Fatalf("siteverify form: secret=%q response=%q ip=%q", gotSecret, gotResponse, gotIP)
	}
}

// TestConfiguredInvalidRejects: success=false → ErrRejected.
func TestConfiguredInvalidRejects(t *testing.T) {
	v := NewTurnstile("s3cr3t", stubClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"success":false,"error-codes":["invalid-input-response"]}`), nil
	}))
	if err := v.Verify(context.Background(), "bad", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("invalid token: got %v, want ErrRejected", err)
	}
}

// TestMissingTokenRejectedWithoutRoundTrip: a blank token is rejected without ever
// calling siteverify (no way to "solve" a challenge you didn't attempt).
func TestMissingTokenRejectedWithoutRoundTrip(t *testing.T) {
	called := false
	v := NewTurnstile("s3cr3t", stubClient(func(*http.Request) (*http.Response, error) {
		called = true
		return jsonResponse(http.StatusOK, `{"success":true}`), nil
	}))
	if err := v.Verify(context.Background(), "", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("blank token: got %v, want ErrRejected", err)
	}
	if called {
		t.Fatal("siteverify was called for a blank token")
	}
}

// TestSiteverifyNetworkErrorFailsClosed: a transport error while CONFIGURED must
// reject (fail closed), never fall open — and it is NOT the ErrRejected sentinel
// (it is a genuine transport failure).
func TestSiteverifyNetworkErrorFailsClosed(t *testing.T) {
	v := NewTurnstile("s3cr3t", stubClient(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	}))
	err := v.Verify(context.Background(), "tok", "")
	if err == nil {
		t.Fatal("network error fell open (nil) — must fail closed")
	}
	if errors.Is(err, ErrRejected) {
		t.Fatal("network error should surface as a transport error, not ErrRejected")
	}
}

// TestSiteverify5xxFailsClosed: a provider 5xx while CONFIGURED must reject.
func TestSiteverify5xxFailsClosed(t *testing.T) {
	v := NewTurnstile("s3cr3t", stubClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusServiceUnavailable, `upstream down`), nil
	}))
	if err := v.Verify(context.Background(), "tok", ""); err == nil {
		t.Fatal("5xx fell open (nil) — must fail closed")
	}
}

// TestMiddlewarePassAndReject: the middleware forwards to next on pass and writes
// 403 on reject, reading the token from the X-Captcha-Token header.
func TestMiddlewarePassAndReject(t *testing.T) {
	resolver := httpx.NewClientIPResolver(nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	// Pass: no-op verifier forwards to next.
	rec := httptest.NewRecorder()
	Middleware(NewNoop(), resolver)(next).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/login", http.NoBody))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("noop middleware: got %d, want 418 (forwarded)", rec.Code)
	}

	// Reject: configured verifier with a blank token → 403, next never runs.
	v := NewTurnstile("s3cr3t", stubClient(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"success":true}`), nil
	}))
	rec = httptest.NewRecorder()
	Middleware(v, resolver)(next).ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/login", http.NoBody))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reject middleware: got %d, want 403", rec.Code)
	}
}
