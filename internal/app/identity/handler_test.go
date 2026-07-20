package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func newTestHandler(store *fakeStore) *Handler {
	svc := NewService(store, time.Hour)
	return NewHandler(svc, []byte("test-secret-test-secret"), 15*time.Minute, 30*24*time.Hour, false, "", nil)
}

func doRequest(h http.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "203.0.113.10:54321" // exercises the host:port -> bare-IP strip
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestRegisterShortPasswordReturns400(t *testing.T) {
	h := newTestHandler(newFakeStore())
	w := doRequest(h.register, http.MethodPost, "/register", map[string]string{
		"workspace_name": "Acme", "email": "owner@acme.test", "password": "short",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterMissingFieldsReturns400(t *testing.T) {
	h := newTestHandler(newFakeStore())
	w := doRequest(h.register, http.MethodPost, "/register", map[string]string{
		"email": "owner@acme.test", "password": "longenoughpw",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLoginBadCredentialsReturns401(t *testing.T) {
	store := newFakeStore()
	hash, err := auth.HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	store.users["user@acme.test"] = gen.User{ID: uuid.New(), Email: "user@acme.test", PasswordHash: hash}

	h := newTestHandler(store)
	w := doRequest(h.login, http.MethodPost, "/login", map[string]string{
		"email": "user@acme.test", "password": "wrong-password",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestLoginUnknownEmailReturns401(t *testing.T) {
	h := newTestHandler(newFakeStore())
	w := doRequest(h.login, http.MethodPost, "/login", map[string]string{
		"email": "nobody@acme.test", "password": "whatever-pw",
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterSuccessSetsRefreshCookieAndToken(t *testing.T) {
	h := newTestHandler(newFakeStore())
	w := doRequest(h.register, http.MethodPost, "/register", map[string]string{
		"workspace_name": "Acme", "email": "owner@acme.test", "password": "s3cret-pw",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("expected non-empty access_token")
	}
	if resp.Role != "owner" {
		t.Fatalf("expected role owner, got %q", resp.Role)
	}
	if len(resp.Memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d", len(resp.Memberships))
	}

	cookies := w.Result().Cookies()
	var refreshCookie, csrfCookie *http.Cookie
	for _, c := range cookies {
		switch c.Name {
		case refreshCookieName:
			refreshCookie = c
		case auth.CSRFCookieName:
			csrfCookie = c
		}
	}
	if refreshCookie == nil {
		t.Fatal("expected inroad_refresh cookie to be set")
	}
	if refreshCookie.Value == "" {
		t.Fatal("expected non-empty refresh cookie value")
	}
	if !refreshCookie.HttpOnly {
		t.Fatal("expected refresh cookie to be httpOnly")
	}
	if csrfCookie == nil {
		t.Fatal("expected csrf cookie to be set")
	}
	if csrfCookie.HttpOnly {
		t.Fatal("expected csrf cookie to be readable (not httpOnly)")
	}
}

func TestRegisterDuplicateEmailReturns409(t *testing.T) {
	store := newFakeStore()
	// Use a real *pgconn.PgError so isUniqueViolation's errors.As path fires —
	// the substring-fallback branch that used to match on "23505" was
	// removed (a caller-influenced error message shouldn't be enough to
	// coax a 409 out of the server).
	store.registerErr = &pgconn.PgError{Code: "23505"}

	h := newTestHandler(store)
	w := doRequest(h.register, http.MethodPost, "/register", map[string]string{
		"workspace_name": "Acme", "email": "owner@acme.test", "password": "s3cret-pw",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRefreshFailureClearsCookiesAndReturns401(t *testing.T) {
	h := newTestHandler(newFakeStore())
	req := httptest.NewRequest(http.MethodPost, "/refresh", nil)
	req.RemoteAddr = "203.0.113.10:54321"
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: "not-a-real-token"})
	w := httptest.NewRecorder()
	h.refresh(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == refreshCookieName && c.MaxAge >= 0 {
			t.Fatal("expected refresh cookie to be cleared (MaxAge < 0)")
		}
	}
}

// TestSwitchWorkspaceUsesSessionIDFromJWTNotBody guards against a
// session-repointing IDOR: switchWorkspace must repoint the session tied to
// the caller's access token (claims.SessionID), never a session id supplied
// in the request body. The request body only carries workspace_id, so this
// test drives the handler through auth.RequireAuth (as it is mounted in
// ProtectedRoutes) and confirms the session named in the JWT is the one that
// gets repointed - a body cannot smuggle in a different session id because
// there is no such field to smuggle it through.
func TestSwitchWorkspaceUsesSessionIDFromJWTNotBody(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(store)

	reg, err := h.svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Acme", Email: "owner@acme.test", Password: "s3cret-pw", UserAgent: "ua", IP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Give the same user membership in a second workspace to switch into.
	otherWS := uuid.New()
	member := gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: otherWS, UserID: reg.UserID, Role: gen.MemberRoleMember}
	store.memberByPair[[2]uuid.UUID{otherWS, reg.UserID}] = member
	store.members[reg.UserID] = append(store.members[reg.UserID], gen.ListMembersByUserRow{
		ID: member.ID, WorkspaceID: otherWS, UserID: reg.UserID, Role: gen.MemberRoleMember, WorkspaceName: "Other",
	})

	access, err := auth.IssueToken(h.jwtSecret, auth.Claims{
		UserID: reg.UserID.String(), WorkspaceID: reg.WorkspaceID.String(), Role: reg.Role, SessionID: reg.SessionID.String(),
	}, h.accessTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"workspace_id": otherWS.String()})
	req := httptest.NewRequest(http.MethodPost, "/switch-workspace", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+access)
	w := httptest.NewRecorder()

	protected := auth.RequireAuth(h.jwtSecret)(http.HandlerFunc(h.switchWorkspace))
	protected.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// The original session row (reg.SessionID) must be the one repointed.
	row, ok := store.sessions[reg.SessionID]
	if !ok {
		t.Fatal("expected original session to still exist")
	}
	if row.WorkspaceID != otherWS {
		t.Fatalf("expected session %s repointed to %s, got %s", reg.SessionID, otherWS, row.WorkspaceID)
	}
}
