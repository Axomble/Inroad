package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireRoleAllowsSufficient(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireRole("admin")(next)
	r := httptest.NewRequest("GET", "/x", nil).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Claims{Role: "owner"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("owner should satisfy admin, got %d", w.Code)
	}
}

func TestRequireRoleRejectsInsufficient(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireRole("admin")(next)
	r := httptest.NewRequest("GET", "/x", nil).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Claims{Role: "member"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member should not satisfy admin, got %d", w.Code)
	}
}
