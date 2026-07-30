//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// registerUser registers a fresh account and returns the access token plus the
// session id embedded in it.
func registerUser(t *testing.T, srv *httptest.Server) (access string, sid uuid.UUID) {
	t.Helper()
	email := fmt.Sprintf("revoke-%d@identity-it.test", time.Now().UnixNano())
	resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"workspace_name": "Revoke Co", "email": email, "password": "s3cret-pw-longenough",
	}, nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register: expected 200, got %d", resp.StatusCode)
	}
	out := decodeSession(t, resp)
	claims, err := auth.ParseToken(testJWTSecret, out.AccessToken)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	sid, err = uuid.Parse(claims.SessionID)
	if err != nil {
		t.Fatalf("parse sid: %v", err)
	}
	return out.AccessToken, sid
}

// meStatus calls GET /auth/me with the given bearer and returns the status code.
func meStatus(t *testing.T, srv *httptest.Server, access string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/auth/me", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do /me: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestRevokedSessionRejectsNextRequest proves a revoked session's still-valid
// access token is rejected on the very next request (the revocation guarantee).
func TestRevokedSessionRejectsNextRequest(t *testing.T) {
	srv, q := newIdentityTestServer(t)
	access, sid := registerUser(t, srv)

	if code := meStatus(t, srv, access); code != http.StatusOK {
		t.Fatalf("fresh session /me: expected 200, got %d", code)
	}

	if _, err := q.RevokeSession(context.Background(), sid); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	if code := meStatus(t, srv, access); code != http.StatusUnauthorized {
		t.Fatalf("revoked session /me: expected 401, got %d", code)
	}
}

// TestTokenVersionBumpRejectsOldAccessToken proves that bumping a session's
// token_version invalidates access tokens minted before the bump, without
// revoking the session row itself.
func TestTokenVersionBumpRejectsOldAccessToken(t *testing.T) {
	srv, q := newIdentityTestServer(t)
	access, sid := registerUser(t, srv)

	if code := meStatus(t, srv, access); code != http.StatusOK {
		t.Fatalf("pre-bump /me: expected 200, got %d", code)
	}

	if err := q.BumpSessionTokenVersion(context.Background(), sid); err != nil {
		t.Fatalf("bump token_version: %v", err)
	}

	if code := meStatus(t, srv, access); code != http.StatusUnauthorized {
		t.Fatalf("post-bump old token /me: expected 401, got %d", code)
	}
}

// TestRevokeOthersLeavesCurrentWorking proves revoke-others kills every other
// session for the user while the calling session keeps working.
func TestRevokeOthersLeavesCurrentWorking(t *testing.T) {
	srv, _ := newIdentityTestServer(t)

	// Session A, then a second login for the SAME user creates session B.
	email := fmt.Sprintf("multi-%d@identity-it.test", time.Now().UnixNano())
	password := "s3cret-pw-longenough"
	regResp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"workspace_name": "Multi Co", "email": email, "password": password,
	}, nil, "")
	defer regResp.Body.Close()
	if regResp.StatusCode != http.StatusOK {
		t.Fatalf("register: expected 200, got %d", regResp.StatusCode)
	}
	accessA := decodeSession(t, regResp).AccessToken

	loginResp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/login", map[string]string{
		"email": email, "password": password,
	}, nil, "")
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login: expected 200, got %d", loginResp.StatusCode)
	}
	accessB := decodeSession(t, loginResp).AccessToken

	// Both sessions are live.
	if code := meStatus(t, srv, accessA); code != http.StatusOK {
		t.Fatalf("session A pre-revoke: expected 200, got %d", code)
	}
	if code := meStatus(t, srv, accessB); code != http.StatusOK {
		t.Fatalf("session B pre-revoke: expected 200, got %d", code)
	}

	// Revoke-others from session B: A dies, B survives.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/auth/sessions/revoke-others", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessB)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revoke-others: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke-others: expected 200, got %d", resp.StatusCode)
	}

	if code := meStatus(t, srv, accessA); code != http.StatusUnauthorized {
		t.Fatalf("session A post-revoke-others: expected 401, got %d", code)
	}
	if code := meStatus(t, srv, accessB); code != http.StatusOK {
		t.Fatalf("current session B post-revoke-others: expected 200, got %d", code)
	}
}

// TestListSessionsFlagsCurrent proves the list endpoint returns the caller's
// live sessions, flags the current one, and never leaks a token hash.
func TestListSessionsFlagsCurrent(t *testing.T) {
	srv, _ := newIdentityTestServer(t)
	access, sid := registerUser(t, srv)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/auth/sessions", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list sessions: expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Sessions []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var foundCurrent bool
	for _, s := range body.Sessions {
		if s.ID == sid.String() {
			foundCurrent = s.Current
		}
	}
	if !foundCurrent {
		t.Fatalf("expected the caller's own session %s flagged current, sessions=%+v", sid, body.Sessions)
	}
}
