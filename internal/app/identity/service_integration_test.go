//go:build integration

package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

var testJWTSecret = []byte("test-secret-test-secret-test-secret")

const (
	testAccessTTL  = 15 * time.Minute
	testRefreshTTL = 30 * 24 * time.Hour
)

// sessionOut mirrors the identity handler's sessionResponse JSON shape so
// tests can decode register/login/refresh bodies without depending on
// unexported types across files in a way that hides field names.
type sessionOut struct {
	AccessToken       string `json:"access_token"`
	ExpiresIn         int    `json:"expires_in"`
	UserID            string `json:"user_id"`
	ActiveWorkspaceID string `json:"active_workspace_id"`
	Role              string `json:"role"`
	Memberships       []struct {
		WorkspaceID   string `json:"workspace_id"`
		WorkspaceName string `json:"workspace_name"`
		Role          string `json:"role"`
	} `json:"memberships"`
}

// newIdentityTestServer wires the identity handler exactly as cmd/inroad/main.go
// does (NewStore -> NewService -> NewHandler -> Routes(secret)), mounted at
// /api/v1/auth on a real httptest.Server backed by a real Postgres pool.
func newIdentityTestServer(t *testing.T) (*httptest.Server, *gen.Queries) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	h := NewHandler(
		NewService(NewStore(pool), testRefreshTTL, &fakeSender{}, "https://app.example.test", time.Hour, time.Hour, time.Hour),
		testJWTSecret, testAccessTTL, testRefreshTTL, false, "", nil,
	)

	r := chi.NewRouter()
	r.Mount("/api/v1/auth", h.Routes(testJWTSecret))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	return srv, gen.New(pool)
}

// findCookie returns the named cookie from resp, or nil if absent.
func findCookie(resp *http.Response, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// jsonRequest performs method/path against srv with an optional JSON body,
// optional cookies to attach, and an optional CSRF header value.
func jsonRequest(t *testing.T, srv *httptest.Server, method, path string, body any, cookies []*http.Cookie, csrfHeader string) *http.Response {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if csrfHeader != "" {
		req.Header.Set(auth.CSRFHeaderName, csrfHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request %s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeSession(t *testing.T, resp *http.Response) sessionOut {
	t.Helper()
	var out sessionOut
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	return out
}

// TestIdentityAuthFlows exercises the full identity HTTP surface against a
// real Postgres: register, duplicate-email conflict, login, refresh rotation,
// refresh reuse detection (whole-family revocation), deny-by-default on a
// protected route, and workspace-switch authorization.
func TestIdentityAuthFlows(t *testing.T) {
	srv, q := newIdentityTestServer(t)
	ctx := context.Background()

	email := fmt.Sprintf("owner-%d@identity-it.test", time.Now().UnixNano())
	password := "s3cret-pw-longenough"

	var registerAccess, registerRefreshCookie, registerCSRFCookie string
	var userID, wsID uuid.UUID

	t.Run("register", func(t *testing.T) {
		resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
			"workspace_name": "Acme A",
			"email":          email,
			"password":       password,
		}, nil, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		refresh := findCookie(resp, refreshCookieName)
		if refresh == nil || refresh.Value == "" {
			t.Fatal("expected non-empty inroad_refresh cookie")
		}
		if !refresh.HttpOnly {
			t.Fatal("expected refresh cookie to be httpOnly")
		}
		csrf := findCookie(resp, auth.CSRFCookieName)
		if csrf == nil || csrf.Value == "" {
			t.Fatal("expected non-empty csrf_token cookie")
		}

		out := decodeSession(t, resp)
		if out.AccessToken == "" {
			t.Fatal("expected non-empty access_token")
		}
		if out.Role != "owner" {
			t.Fatalf("expected role owner, got %q", out.Role)
		}

		registerAccess = out.AccessToken
		registerRefreshCookie = refresh.Value
		registerCSRFCookie = csrf.Value

		var err error
		userID, err = uuid.Parse(out.UserID)
		if err != nil {
			t.Fatalf("parse user id: %v", err)
		}
		wsID, err = uuid.Parse(out.ActiveWorkspaceID)
		if err != nil {
			t.Fatalf("parse workspace id: %v", err)
		}

		// Verify real rows landed in Postgres: user, workspace, and an
		// owner membership linking them.
		user, err := q.GetUserByEmail(ctx, email)
		if err != nil {
			t.Fatalf("GetUserByEmail: %v", err)
		}
		if user.ID != userID {
			t.Fatalf("expected persisted user id %s, got %s", userID, user.ID)
		}
		member, err := q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: wsID, UserID: userID})
		if err != nil {
			t.Fatalf("GetMember: %v", err)
		}
		if member.Role != gen.MemberRoleOwner {
			t.Fatalf("expected owner role in DB, got %q", member.Role)
		}
	})

	t.Run("duplicate email register returns 409", func(t *testing.T) {
		resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
			"workspace_name": "Acme A Duplicate",
			"email":          email,
			"password":       "another-longenough-pw",
		}, nil, "")
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("expected 409, got %d", resp.StatusCode)
		}
	})

	t.Run("login", func(t *testing.T) {
		resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/login", map[string]string{
			"email":    email,
			"password": password,
		}, nil, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		out := decodeSession(t, resp)
		if out.AccessToken == "" {
			t.Fatal("expected non-empty access_token")
		}
		if len(out.Memberships) != 1 {
			t.Fatalf("expected 1 membership, got %d", len(out.Memberships))
		}
		if out.Memberships[0].WorkspaceID != wsID.String() {
			t.Fatalf("expected membership workspace %s, got %s", wsID, out.Memberships[0].WorkspaceID)
		}
	})

	var rotatedRefreshCookie string

	t.Run("refresh rotates the session", func(t *testing.T) {
		cookies := []*http.Cookie{
			{Name: refreshCookieName, Value: registerRefreshCookie},
			{Name: auth.CSRFCookieName, Value: registerCSRFCookie},
		}
		resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/refresh", nil, cookies, registerCSRFCookie)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		out := decodeSession(t, resp)
		if out.AccessToken == "" || out.AccessToken == registerAccess {
			t.Fatalf("expected a NEW non-empty access token, got %q (orig %q)", out.AccessToken, registerAccess)
		}
		newRefresh := findCookie(resp, refreshCookieName)
		if newRefresh == nil || newRefresh.Value == "" {
			t.Fatal("expected a rotated inroad_refresh cookie")
		}
		if newRefresh.Value == registerRefreshCookie {
			t.Fatal("expected rotated refresh cookie to differ from the original")
		}
		rotatedRefreshCookie = newRefresh.Value
	})

	t.Run("refresh reuse is detected and revokes the family", func(t *testing.T) {
		// Replay the ORIGINAL pre-rotation refresh cookie.
		cookies := []*http.Cookie{
			{Name: refreshCookieName, Value: registerRefreshCookie},
			{Name: auth.CSRFCookieName, Value: registerCSRFCookie},
		}
		resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/refresh", nil, cookies, registerCSRFCookie)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 on reused refresh token, got %d", resp.StatusCode)
		}

		// The whole family is now revoked: even the rotated (legitimately
		// issued) cookie from the previous subtest must now fail too.
		cookies2 := []*http.Cookie{
			{Name: refreshCookieName, Value: rotatedRefreshCookie},
			{Name: auth.CSRFCookieName, Value: registerCSRFCookie},
		}
		resp2 := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/refresh", nil, cookies2, registerCSRFCookie)
		if resp2.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, entire family should be revoked after reuse, got %d", resp2.StatusCode)
		}

		// Cross-check directly against the DB: every session in the family
		// tied to this user's workspace is revoked.
		sessRow, err := q.GetSessionByHash(ctx, auth.HashRefreshToken(rotatedRefreshCookie))
		if err != nil {
			t.Fatalf("GetSessionByHash: %v", err)
		}
		if !sessRow.RevokedAt.Valid {
			t.Fatal("expected rotated session row to be marked revoked in the DB")
		}
	})

	t.Run("deny by default: /me with no Authorization header", func(t *testing.T) {
		resp := jsonRequest(t, srv, http.MethodGet, "/api/v1/auth/me", nil, nil, "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", resp.StatusCode)
		}
	})

	t.Run("switch-workspace to a non-member workspace is forbidden", func(t *testing.T) {
		// Create a second, unrelated workspace + owner user via a second
		// register call; user A (registerAccess) is not a member of it.
		otherEmail := fmt.Sprintf("owner-b-%d@identity-it.test", time.Now().UnixNano())
		regResp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
			"workspace_name": "Acme B",
			"email":          otherEmail,
			"password":       "another-longenough-pw",
		}, nil, "")
		if regResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 registering second workspace, got %d", regResp.StatusCode)
		}
		otherOut := decodeSession(t, regResp)

		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/switch-workspace",
			bytes.NewReader(mustJSON(t, map[string]string{"workspace_id": otherOut.ActiveWorkspaceID})))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+registerAccess)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 switching into a workspace user A is not a member of, got %d", resp.StatusCode)
		}
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
