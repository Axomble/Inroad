package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireCSRFMatch(t *testing.T) {
	h := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	r := httptest.NewRequest("POST", "/auth/refresh", nil)
	r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "tok123"})
	r.Header.Set("X-CSRF-Token", "tok123")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
}

func TestRequireCSRFMismatch(t *testing.T) {
	h := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	r := httptest.NewRequest("POST", "/auth/refresh", nil)
	r.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "tok123"})
	r.Header.Set("X-CSRF-Token", "different")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}
