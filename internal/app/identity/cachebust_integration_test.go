//go:build integration

package identity

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// registered bundles everything a test needs off a fresh register: the access
// token, the session id it names, the owning user id, and the refresh + CSRF
// cookie values (the CSRF-guarded logout endpoint needs the latter two).
type registered struct {
	access        string
	sid           uuid.UUID
	userID        uuid.UUID
	refreshCookie string
	csrfCookie    string
}

// registerFull registers a fresh account and returns its access token, session
// id, user id, and the refresh/CSRF cookie values.
func registerFull(t *testing.T, srv *httptest.Server) registered {
	t.Helper()
	email := fmt.Sprintf("bust-%d@identity-it.test", time.Now().UnixNano())
	resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"workspace_name": "Bust Co", "email": email, "password": "s3cret-pw-longenough",
	}, nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register: expected 200, got %d", resp.StatusCode)
	}
	refresh := findCookie(resp, refreshCookieName)
	csrf := findCookie(resp, auth.CSRFCookieName)
	if refresh == nil || csrf == nil {
		t.Fatal("register: expected refresh + csrf cookies")
	}
	out := decodeSession(t, resp)
	claims, err := auth.ParseToken(testJWTSecret, out.AccessToken)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	sid, err := uuid.Parse(claims.SessionID)
	if err != nil {
		t.Fatalf("parse sid: %v", err)
	}
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		t.Fatalf("parse uid: %v", err)
	}
	return registered{access: out.AccessToken, sid: sid, userID: uid, refreshCookie: refresh.Value, csrfCookie: csrf.Value}
}

// TestLogoutBustsSessionCache proves logout invalidates the verifier's WARM
// cache in-process: with a long cache TTL the just-logged-out access token is
// rejected on its very next request, not after the TTL. This is the regression
// guard for the pre-fix gap where logout revoked the session in the DB but never
// busted the cache, so the access token stayed valid for up to the TTL.
func TestLogoutBustsSessionCache(t *testing.T) {
	srv, _ := newIdentityTestServerTTL(t, time.Minute)
	reg := registerFull(t, srv)

	// Warm the cache: this /me populates the verifier's cached "live" state.
	if code := meStatus(t, srv, reg.access); code != http.StatusOK {
		t.Fatalf("pre-logout /me: expected 200, got %d", code)
	}

	cookies := []*http.Cookie{
		{Name: refreshCookieName, Value: reg.refreshCookie},
		{Name: auth.CSRFCookieName, Value: reg.csrfCookie},
	}
	resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/logout", nil, cookies, reg.csrfCookie)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: expected 200, got %d", resp.StatusCode)
	}

	if code := meStatus(t, srv, reg.access); code != http.StatusUnauthorized {
		t.Fatalf("post-logout /me: expected prompt 401 (cache busted), got %d", code)
	}
}

// TestLogoutAllBustsSessionCache proves logout-everywhere busts the warm cache
// for the caller's own session (RevokeAllForUser includes it), so its access
// token is rejected immediately rather than after the cache TTL.
func TestLogoutAllBustsSessionCache(t *testing.T) {
	srv, _ := newIdentityTestServerTTL(t, time.Minute)
	reg := registerFull(t, srv)

	if code := meStatus(t, srv, reg.access); code != http.StatusOK {
		t.Fatalf("pre-logout-all /me: expected 200, got %d", code)
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/auth/logout-all", http.NoBody)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+reg.access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("logout-all: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout-all: expected 200, got %d", resp.StatusCode)
	}

	if code := meStatus(t, srv, reg.access); code != http.StatusUnauthorized {
		t.Fatalf("post-logout-all /me: expected prompt 401 (cache busted), got %d", code)
	}
}

// TestPasswordResetBustsSessionCache proves a password reset busts the warm
// cache for every revoked session, so an access token minted before the reset is
// rejected immediately in-process. The reset token is created directly against
// the store (same hash the emailed link would carry) so the test doesn't depend
// on the fake sender.
func TestPasswordResetBustsSessionCache(t *testing.T) {
	srv, q := newIdentityTestServerTTL(t, time.Minute)
	reg := registerFull(t, srv)

	if code := meStatus(t, srv, reg.access); code != http.StatusOK {
		t.Fatalf("pre-reset /me: expected 200, got %d", code)
	}

	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken: %v", err)
	}
	if _, err := q.CreateUserToken(context.Background(), gen.CreateUserTokenParams{
		UserID:    reg.userID,
		Kind:      gen.UserTokenKindPasswordReset,
		TokenHash: hash,
		ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)),
	}); err != nil {
		t.Fatalf("CreateUserToken: %v", err)
	}

	resp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/password/reset", map[string]string{
		"token": raw, "new_password": "a-brand-new-pw-longenough",
	}, nil, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("password reset: expected 204, got %d", resp.StatusCode)
	}

	if code := meStatus(t, srv, reg.access); code != http.StatusUnauthorized {
		t.Fatalf("post-reset /me: expected prompt 401 (cache busted), got %d", code)
	}
}

// TestSwitchWorkspaceSourcesTokenVersion proves switch-workspace re-mints the
// access token with the session's LIVE token_version, not a hardcoded 0. The
// session's token_version is bumped out of band to 1 (and a matching tv=1 access
// token minted, as the caller would legitimately hold after such a bump); after
// switching, the freshly issued token must carry tv=1 AND be accepted by the
// verifier. Under the pre-fix code the re-issued token carried tv=0 and would be
// rejected on its next request — an instant self-inflicted 401.
func TestSwitchWorkspaceSourcesTokenVersion(t *testing.T) {
	srv, q := newIdentityTestServer(t) // TTL 0: verifier reads live tv every request
	reg := registerFull(t, srv)
	ctx := context.Background()

	// A second workspace the same user owns, to switch into.
	otherEmail := fmt.Sprintf("switch-other-%d@identity-it.test", time.Now().UnixNano())
	otherResp := jsonRequest(t, srv, http.MethodPost, "/api/v1/auth/register", map[string]string{
		"workspace_name": "Switch Other", "email": otherEmail, "password": "s3cret-pw-longenough",
	}, nil, "")
	defer otherResp.Body.Close()
	otherWS := decodeSession(t, otherResp).ActiveWorkspaceID
	otherWSID, err := uuid.Parse(otherWS)
	if err != nil {
		t.Fatalf("parse other ws id: %v", err)
	}
	// Add reg's user as a member of the other workspace so the switch is allowed.
	if _, err := q.CreateMember(ctx, gen.CreateMemberParams{
		WorkspaceID: otherWSID, UserID: reg.userID, Role: gen.MemberRoleMember,
	}); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}

	// Bump the caller's session token_version to 1 out of band, and mint the
	// tv=1 access token the caller would hold right after that bump (their old
	// tv=0 token is now stale and would be rejected by RequireAuth).
	if err := q.BumpSessionTokenVersion(ctx, reg.sid); err != nil {
		t.Fatalf("BumpSessionTokenVersion: %v", err)
	}
	tv1Access, err := auth.IssueToken(testJWTSecret, auth.Claims{
		UserID: reg.userID.String(), WorkspaceID: reg.sid.String(), Role: "owner",
		SessionID: reg.sid.String(), TokenVersion: 1,
	}, testAccessTTL)
	if err != nil {
		t.Fatalf("IssueToken tv=1: %v", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/v1/auth/switch-workspace",
		bytes.NewReader(mustJSON(t, map[string]string{"workspace_id": otherWS})))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tv1Access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("switch-workspace: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switch-workspace: expected 200, got %d", resp.StatusCode)
	}
	switched := decodeSession(t, resp)

	// The re-issued token must carry the session's live tv (1), not 0.
	newClaims, err := auth.ParseToken(testJWTSecret, switched.AccessToken)
	if err != nil {
		t.Fatalf("parse re-issued token: %v", err)
	}
	if newClaims.TokenVersion != 1 {
		t.Fatalf("expected re-issued token tv=1 (sourced from the session row), got %d", newClaims.TokenVersion)
	}
	// ...and it must actually be accepted (tv matches the row), not self-401.
	if code := meStatus(t, srv, switched.AccessToken); code != http.StatusOK {
		t.Fatalf("re-issued switch token /me: expected 200, got %d", code)
	}
}
