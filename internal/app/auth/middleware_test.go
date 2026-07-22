package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
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

func TestRequireRolePanicsOnUnknownRole(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RequireRole should panic on unknown role")
		}
	}()
	RequireRole("nonexistent-role")
}

// fakeVerifiedChecker is a test double for VerifiedChecker: it returns the
// configured verified/err pair regardless of which user id is asked about.
type fakeVerifiedChecker struct {
	verified bool
	err      error
}

func (f fakeVerifiedChecker) IsEmailVerified(_ context.Context, _ uuid.UUID) (bool, error) {
	return f.verified, f.err
}

func TestRequireVerifiedAllowsVerifiedUser(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireVerified(fakeVerifiedChecker{verified: true})(next)
	r := httptest.NewRequest("POST", "/x", nil).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Claims{UserID: uuid.New().String()}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("verified user should reach next handler, got %d", w.Code)
	}
}

func TestRequireVerifiedRejectsUnverifiedUser(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireVerified(fakeVerifiedChecker{verified: false})(next)
	r := httptest.NewRequest("POST", "/x", nil).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Claims{UserID: uuid.New().String()}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unverified user should get 403, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "email_not_verified") {
		t.Fatalf("expected email_not_verified in body, got %q", w.Body.String())
	}
}

func TestRequireVerifiedRejectsMissingClaims(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireVerified(fakeVerifiedChecker{verified: true})(next)
	r := httptest.NewRequest("POST", "/x", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing claims should get 401, got %d", w.Code)
	}
}

func TestRequireVerifiedRejectsUnparseableUserID(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireVerified(fakeVerifiedChecker{verified: true})(next)
	r := httptest.NewRequest("POST", "/x", nil).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Claims{UserID: "not-a-uuid"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unparseable user id should get 401, got %d", w.Code)
	}
}

func TestRequireVerifiedReturns500OnCheckerError(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	h := RequireVerified(fakeVerifiedChecker{err: errors.New("boom")})(next)
	r := httptest.NewRequest("POST", "/x", nil).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Claims{UserID: uuid.New().String()}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("checker error should get 500, got %d", w.Code)
	}
}
