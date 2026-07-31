package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mailboxes", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	for _, name := range []string{
		"Content-Security-Policy", "Permissions-Policy", "Referrer-Policy",
		"X-Content-Type-Options", "X-Frame-Options",
	} {
		if rec.Header().Get(name) == "" {
			t.Errorf("%s header is missing", name)
		}
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestLimitRequestBody(t *testing.T) {
	h := limitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/campaigns", strings.NewReader(strings.Repeat("x", maxJSONBody+1)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}
