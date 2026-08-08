package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// A password NULL in the database means "this account has no password". It must
// reject every presented password — including the empty string, which is the one
// value a naive `CheckPassword(coalesce(hash,”), pw)` would be most likely to let
// through.
func TestAuthenticateRejectsNullPasswordHash(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	svc := newGoogleService(t, store, g)

	sess, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, ""), "ua", "")
	if err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}
	if store.usersByID[sess.UserID].PasswordHash != nil {
		t.Fatal("precondition: the federated account should have no password hash")
	}

	for _, pw := range []string{"", " ", "password", "s3cret-pw"} {
		if _, err := svc.Authenticate(context.Background(), "newperson@axomble.com", pw); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("password %q: want ErrInvalidCredentials, got %v", pw, err)
		}
	}
}

// A password reset legitimately GIVES a federated account a password (account
// recovery for an address the provider already verified) — and from then on
// password login works. This pins the boundary of the check above: NULL rejects,
// a real hash does not.
func TestFederatedAccountCanGainAPasswordViaReset(t *testing.T) {
	store := newFakeStore()
	svc := newGoogleService(t, store, &fakeGoogle{enabled: true, identity: verifiedIdentity()})

	sess, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, ""), "ua", "")
	if err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}
	raw, err := store.IssueUserToken(context.Background(), sess.UserID, "password_reset", time.Hour)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}
	if _, err := svc.ResetPassword(context.Background(), raw, "brand-new-password-456"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	uid, err := svc.Authenticate(context.Background(), "newperson@axomble.com", "brand-new-password-456")
	if err != nil {
		t.Fatalf("Authenticate after reset: %v", err)
	}
	if uid != sess.UserID {
		t.Fatalf("want user %s, got %s", sess.UserID, uid)
	}
}

func TestCompleteOnboardingSetsNameAndStamp(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	reg, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Dana's workspace", Email: "dana@axomble.com", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !reg.OnboardingCompletedAt.IsZero() {
		t.Fatalf("a fresh workspace must report onboarding as pending, got %v", reg.OnboardingCompletedAt)
	}

	res, err := svc.CompleteOnboarding(context.Background(), reg.WorkspaceID, "Axomble Inc")
	if err != nil {
		t.Fatalf("CompleteOnboarding: %v", err)
	}
	if res.Name != "Axomble Inc" {
		t.Fatalf("want the workspace renamed, got %q", res.Name)
	}
	if res.CompletedAt.IsZero() {
		t.Fatal("want onboarding stamped complete")
	}
}

// Idempotent: a replayed request returns the existing row and does NOT rename.
// Otherwise a double-clicked (or retried) onboarding submit could overwrite a name
// the workspace has since been given deliberately.
func TestCompleteOnboardingIsIdempotentAndDoesNotRename(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	reg, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Dana's workspace", Email: "dana@axomble.com", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	first, err := svc.CompleteOnboarding(context.Background(), reg.WorkspaceID, "Axomble Inc")
	if err != nil {
		t.Fatalf("first complete: %v", err)
	}
	second, err := svc.CompleteOnboarding(context.Background(), reg.WorkspaceID, "Something Else")
	if err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if second.Name != "Axomble Inc" {
		t.Fatalf("a replay must not rename; got %q", second.Name)
	}
	if !second.CompletedAt.Equal(first.CompletedAt) {
		t.Fatalf("a replay must keep the original timestamp: %v vs %v", second.CompletedAt, first.CompletedAt)
	}
}

func TestCompleteOnboardingUnknownWorkspace(t *testing.T) {
	svc := newTestService(newFakeStore())
	if _, err := svc.CompleteOnboarding(context.Background(), uuid.New(), "Whatever"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("want ErrWorkspaceNotFound, got %v", err)
	}
}

// Once complete, every auth response reports it — so the SPA never shows the
// onboarding modal to a workspace that has already been named.
func TestSessionsReportOnboardingStateAfterCompletion(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	reg, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Dana's workspace", Email: "dana@axomble.com", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := svc.CompleteOnboarding(context.Background(), reg.WorkspaceID, "Axomble Inc"); err != nil {
		t.Fatalf("CompleteOnboarding: %v", err)
	}

	sess, err := svc.Login(context.Background(), "dana@axomble.com", "s3cret-pw", "ua", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.OnboardingCompletedAt.IsZero() {
		t.Fatal("want a non-zero onboarding stamp on the session after completion")
	}
	if len(sess.Memberships) != 1 || sess.Memberships[0].OnboardingCompletedAt.IsZero() {
		t.Fatal("want a non-zero onboarding stamp on the membership too")
	}
}

// The onboarding route is admin-and-above only, and pinned to the workspace in the
// caller's token: naming a workspace is tenant configuration, so a plain member
// must not be able to rename the tenant, and an admin of workspace A must not be
// able to rename workspace B by naming it in the path.
func TestOnboardingRouteRejectsNonAdminAndForeignWorkspace(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(store)

	reg, err := h.svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Dana's workspace", Email: "dana@axomble.com", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	call := func(t *testing.T, role, pathWS string) *httptest.ResponseRecorder {
		t.Helper()
		access, err := auth.IssueToken(h.jwtSecret, auth.Claims{
			UserID: reg.UserID.String(), WorkspaceID: reg.WorkspaceID.String(),
			Role: role, SessionID: reg.SessionID.String(),
		}, h.accessTTL)
		if err != nil {
			t.Fatalf("IssueToken: %v", err)
		}
		body, _ := json.Marshal(map[string]string{"name": "Renamed By Force"})
		req := httptest.NewRequest(http.MethodPost, "/"+pathWS+"/onboarding/complete", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+access)
		w := httptest.NewRecorder()
		auth.RequireAuth(auth.NewJWTVerifier(h.jwtSecret))(h.WorkspaceRoutes()).ServeHTTP(w, req)
		return w
	}

	t.Run("member is forbidden", func(t *testing.T) {
		if w := call(t, "member", reg.WorkspaceID.String()); w.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
		}
		if store.workspaces[reg.WorkspaceID].Name != "Dana's workspace" {
			t.Fatal("a rejected request must not rename the workspace")
		}
	})

	t.Run("admin of another workspace is forbidden", func(t *testing.T) {
		if w := call(t, "admin", uuid.NewString()); w.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("owner of this workspace succeeds", func(t *testing.T) {
		w := call(t, "owner", reg.WorkspaceID.String())
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		if bytes.Contains(w.Body.Bytes(), []byte(`"onboarding_completed_at":null`)) {
			t.Fatalf("want a non-null onboarding_completed_at in the body, got %s", w.Body.String())
		}
	})
}
