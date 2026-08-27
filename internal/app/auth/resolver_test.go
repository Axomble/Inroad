package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubVerifier is a Verifier whose result is fixed at construction, recording
// whether it was consulted so tests can assert short-circuit behavior.
type stubVerifier struct {
	principal Principal
	ok        bool
	err       error
	called    *bool
}

func (s stubVerifier) Verify(_ context.Context, _ *http.Request) (Principal, bool, error) {
	if s.called != nil {
		*s.called = true
	}
	return s.principal, s.ok, s.err
}

func serve(t *testing.T, mw func(http.Handler) http.Handler, r *http.Request) (int, Principal) {
	t.Helper()
	var got Principal
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got, _ = UserFromContext(req.Context())
		w.WriteHeader(http.StatusOK)
	})
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, r)
	return w.Code, got
}

func TestRequireAuthFirstOkWins(t *testing.T) {
	secondCalled := false
	want := Principal{UserID: "u1", Kind: KindSession}
	mw := RequireAuth(
		stubVerifier{principal: want, ok: true},
		stubVerifier{called: &secondCalled},
	)
	code, got := serve(t, mw, httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody))
	if code != http.StatusOK {
		t.Fatalf("first-ok should reach next, got %d", code)
	}
	if got.UserID != "u1" {
		t.Fatalf("expected principal from first verifier, got %+v", got)
	}
	if secondCalled {
		t.Fatal("second verifier must not be consulted once the first succeeds")
	}
}

func TestRequireAuthDefersDownChain(t *testing.T) {
	want := Principal{UserID: "u2", Kind: KindAPIKey}
	mw := RequireAuth(
		stubVerifier{ok: false},                 // defer
		stubVerifier{principal: want, ok: true}, // wins
	)
	code, got := serve(t, mw, httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody))
	if code != http.StatusOK || got.UserID != "u2" {
		t.Fatalf("expected defer then second wins, got code=%d principal=%+v", code, got)
	}
}

func TestRequireAuthAllDeferIs401(t *testing.T) {
	mw := RequireAuth(stubVerifier{ok: false}, stubVerifier{ok: false})
	code, _ := serve(t, mw, httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody))
	if code != http.StatusUnauthorized {
		t.Fatalf("all-defer should be 401, got %d", code)
	}
}

func TestRequireAuthHardFailUnauthorizedIs401(t *testing.T) {
	secondCalled := false
	mw := RequireAuth(
		stubVerifier{err: ErrUnauthorized},
		stubVerifier{called: &secondCalled, ok: true},
	)
	code, _ := serve(t, mw, httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody))
	if code != http.StatusUnauthorized {
		t.Fatalf("hard ErrUnauthorized should be 401, got %d", code)
	}
	if secondCalled {
		t.Fatal("a hard failure must stop the chain, not defer")
	}
}

func TestRequireAuthHardFailInternalIs500(t *testing.T) {
	mw := RequireAuth(stubVerifier{err: errors.New("db down")})
	code, _ := serve(t, mw, httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody))
	if code != http.StatusInternalServerError {
		t.Fatalf("non-Unauthorized error should be 500, got %d", code)
	}
}

func TestRequireAuthPanicsWithoutVerifiers(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("RequireAuth with no verifiers should panic")
		}
	}()
	RequireAuth()
}

func TestRequireScopeSessionHoldsAll(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody).WithContext(
		context.WithValue(context.Background(), ctxKey{}, Principal{Kind: KindSession}))
	w := httptest.NewRecorder()
	RequireScope(ScopeMailboxesWrite)(next).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("session principal should hold every scope, got %d", w.Code)
	}
}

func TestRequireScopeMachineGrantedSubset(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	p := Principal{Kind: KindAPIKey, Scopes: []string{ScopeMailboxesRead}}

	r := httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody).WithContext(
		context.WithValue(context.Background(), ctxKey{}, p))
	w := httptest.NewRecorder()
	RequireScope(ScopeMailboxesRead)(next).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("granted scope should pass, got %d", w.Code)
	}

	r2 := httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody).WithContext(
		context.WithValue(context.Background(), ctxKey{}, p))
	w2 := httptest.NewRecorder()
	RequireScope(ScopeMailboxesWrite)(next).ServeHTTP(w2, r2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("ungranted scope should be 403, got %d", w2.Code)
	}
}

func TestRequireScopeRejectsMissingPrincipal(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	w := httptest.NewRecorder()
	RequireScope(ScopeMailboxesRead)(next).ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing principal should be 401, got %d", w.Code)
	}
}

func TestJWTVerifierDefersWithoutBearer(t *testing.T) {
	_, ok, err := NewJWTVerifier([]byte("0123456789abcdef")).Verify(context.Background(),
		httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody))
	if ok || err != nil {
		t.Fatalf("no bearer should defer (false,nil), got ok=%v err=%v", ok, err)
	}
}

func TestJWTVerifierRejectsBadToken(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody)
	r.Header.Set("Authorization", "Bearer not-a-jwt")
	_, ok, err := NewJWTVerifier([]byte("0123456789abcdef")).Verify(context.Background(), r)
	if ok || !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token should hard-fail ErrUnauthorized, got ok=%v err=%v", ok, err)
	}
}

func TestJWTVerifierMapsClaims(t *testing.T) {
	secret := []byte("0123456789abcdef")
	tok, err := IssueToken(secret, Claims{UserID: "u1", WorkspaceID: "w1", Role: "admin", SessionID: "s1"}, time.Minute)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), "GET", "/x", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+tok)
	p, ok, err := NewJWTVerifier(secret).Verify(context.Background(), r)
	if !ok || err != nil {
		t.Fatalf("valid token should authenticate, got ok=%v err=%v", ok, err)
	}
	if p.UserID != "u1" || p.WorkspaceID != "w1" || p.Role != "admin" || p.SessionID != "s1" || p.Kind != KindSession {
		t.Fatalf("principal mismatch: %+v", p)
	}
}
