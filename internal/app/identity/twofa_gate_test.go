package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeGate is a stand-in TwoFactorGate for login-gate handler tests.
type fakeGate struct {
	required  bool
	challenge string
	err       error
	gotUserID uuid.UUID
}

func (g *fakeGate) ChallengeIfRequired(_ context.Context, userID uuid.UUID, _ string) (string, bool, error) {
	g.gotUserID = userID
	if g.err != nil {
		return "", false, g.err
	}
	return g.challenge, g.required, nil
}

// newGatedTestHandler builds a Handler wired with a 2FA gate, over a fakeStore
// seeded with one login-ready user (password + a single workspace membership).
func newGatedTestHandler(t *testing.T, gate TwoFactorGate) (*Handler, string, string) {
	t.Helper()
	store := newFakeStore()
	const email, password = "gate@acme.test", "correct-horse-battery-staple"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	uid := uuid.New()
	wsID := uuid.New()
	store.users[email] = gen.User{ID: uid, Email: email, PasswordHash: &hash}
	store.usersByID[uid] = store.users[email]
	store.members[uid] = []gen.ListMembersByUserRow{
		{ID: uuid.New(), WorkspaceID: wsID, UserID: uid, Role: gen.MemberRoleOwner, WorkspaceName: "Acme"},
	}
	store.memberByPair[[2]uuid.UUID{wsID, uid}] = gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: wsID, UserID: uid, Role: gen.MemberRoleOwner}

	svc := newTestService(store)
	h := NewHandler(svc, []byte("test-secret-test-secret"), 15*time.Minute, 30*24*time.Hour, false, "", nil, nil, gate)
	return h, email, password
}

// TestLoginGateChallengesConfirmedUser proves a confirmed-2FA user gets a 200
// with two_factor_required + a challenge, and crucially NO session (no
// access_token, no refresh cookie) — the fail-closed gate.
func TestLoginGateChallengesConfirmedUser(t *testing.T) {
	gate := &fakeGate{required: true, challenge: "opaque-challenge-token"}
	h, email, password := newGatedTestHandler(t, gate)

	w := doRequest(h.login, http.MethodPost, "/login", map[string]string{"email": email, "password": password})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		TwoFactorRequired bool   `json:"two_factor_required"`
		Challenge         string `json:"challenge"`
		AccessToken       string `json:"access_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.TwoFactorRequired || body.Challenge != "opaque-challenge-token" {
		t.Fatalf("expected two_factor_required with challenge, got %+v", body)
	}
	if body.AccessToken != "" {
		t.Fatal("a gated login must NOT issue an access token")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == refreshCookieName && c.Value != "" && c.MaxAge >= 0 {
			t.Fatal("a gated login must NOT set a refresh cookie")
		}
	}
}

// TestLoginGateNonConfirmedUserGetsSession proves a user WITHOUT a confirmed
// factor logs in unchanged: a full session with an access token.
func TestLoginGateNonConfirmedUserGetsSession(t *testing.T) {
	gate := &fakeGate{required: false}
	h, email, password := newGatedTestHandler(t, gate)

	w := doRequest(h.login, http.MethodPost, "/login", map[string]string{"email": email, "password": password})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp sessionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("a non-2FA login must issue an access token")
	}
}

// TestLoginGateRateLimitedReturns429 proves the per-IP challenge throttle surfaces
// as HTTP 429 (not a generic 500): when the gate returns the shared
// auth.ErrTwoFactorRateLimited sentinel, login maps it to StatusTooManyRequests.
func TestLoginGateRateLimitedReturns429(t *testing.T) {
	gate := &fakeGate{err: auth.ErrTwoFactorRateLimited}
	h, email, password := newGatedTestHandler(t, gate)

	w := doRequest(h.login, http.MethodPost, "/login", map[string]string{"email": email, "password": password})
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", w.Code, w.Body.String())
	}
}

// TestLoginGateNotConsultedOnWrongPassword proves the gate is never consulted
// when the password is wrong — so a wrong password can't reveal 2FA status, and
// the response is a flat 401 identical to a non-2FA account.
func TestLoginGateNotConsultedOnWrongPassword(t *testing.T) {
	gate := &fakeGate{required: true, challenge: "should-not-be-issued"}
	h, email, _ := newGatedTestHandler(t, gate)

	w := doRequest(h.login, http.MethodPost, "/login", map[string]string{"email": email, "password": "wrong-password"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	if gate.gotUserID != uuid.Nil {
		t.Fatal("the 2FA gate must not be consulted on a wrong password")
	}
}
