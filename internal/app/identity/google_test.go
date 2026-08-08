package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var googleStateSecret = []byte("google-state-secret-16-bytes-min")

// newGoogleService wires a Service with the fake Google provider. Everything from
// the authorization code onward is exercised; the code exchange with the real
// accounts.google.com is NOT, and cannot be from a unit test — see fakeGoogle.
func newGoogleService(t *testing.T, store *fakeStore, g *fakeGoogle) *Service {
	t.Helper()
	return NewService(store, time.Hour, &fakeSender{}, "https://app.example.test",
		time.Hour, time.Hour, time.Hour, WithGoogleSignIn(g, googleStateSecret))
}

// startAndState runs the start half and returns the `state` the SPA would have
// been redirected with, pulled out of the fake provider's consent URL.
func startAndState(t *testing.T, svc *Service, inviteToken string) string {
	t.Helper()
	url, err := svc.StartGoogleSignIn(context.Background(), StartGoogleSignInInput{InviteToken: inviteToken})
	if err != nil {
		t.Fatalf("StartGoogleSignIn: %v", err)
	}
	_, state, ok := strings.Cut(url, "state=")
	if !ok {
		t.Fatalf("consent URL carried no state: %q", url)
	}
	return state
}

func verifiedIdentity() GoogleIdentity {
	return GoogleIdentity{
		Subject: "google-sub-1", Email: "newperson@axomble.com", EmailVerified: true,
		GivenName: "Dana", HostedDomain: "axomble.com",
	}
}

// A brand-new person with no invite gets their own workspace, is its owner, has a
// verified email, a linked identity, a session, and onboarding still PENDING.
func TestGoogleSignInCreatesWorkspaceUserMemberAndSession(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	svc := newGoogleService(t, store, g)

	state := startAndState(t, svc, "")
	sess, _, err := svc.CompleteGoogleSignIn(context.Background(), "auth-code", state, "ua", "1.2.3.4")
	if err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}

	if sess.Role != "owner" {
		t.Fatalf("want role owner, got %q", sess.Role)
	}
	if sess.Email != "newperson@axomble.com" {
		t.Fatalf("want the provider email on the session, got %q", sess.Email)
	}
	if sess.RawRefresh == "" || sess.SessionID == uuid.Nil {
		t.Fatal("want a session with a refresh token")
	}
	if !sess.OnboardingCompletedAt.IsZero() {
		t.Fatalf("a brand-new workspace must report onboarding as PENDING, got %v", sess.OnboardingCompletedAt)
	}
	if got := store.workspaces[sess.WorkspaceID].Name; got != "Axomble" {
		t.Fatalf("want the workspace named from the hd claim, got %q", got)
	}
	user := store.usersByID[sess.UserID]
	if user.PasswordHash != nil {
		t.Fatal("a federated signup must have NO password hash")
	}
	if !user.EmailVerifiedAt.Valid {
		t.Fatal("want email_verified_at set from the provider claim")
	}
	if _, ok := store.identities[identityKey(ProviderGoogle, "google-sub-1")]; !ok {
		t.Fatal("want the google identity linked to the new user")
	}
	if _, ok := store.memberByPair[[2]uuid.UUID{sess.WorkspaceID, sess.UserID}]; !ok {
		t.Fatal("want an owner membership in the new workspace")
	}
}

// The state is consumed on first use: a second callback with the SAME state is
// rejected, and nothing is created. This is the whole point of the server-side
// nonce store — for a login flow, replaying state is account access.
func TestGoogleSignInStateIsSingleUse(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	svc := newGoogleService(t, store, g)

	state := startAndState(t, svc, "")
	if _, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", state, "ua", ""); err != nil {
		t.Fatalf("first use: %v", err)
	}
	before := len(store.workspaces)

	_, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", state, "ua", "")
	if !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("want ErrStateInvalid on the second use, got %v", err)
	}
	if len(store.workspaces) != before {
		t.Fatal("a replayed state must not create anything")
	}
}

// A state that was never minted here (or was tampered with) never reaches the
// database as a lookup key.
func TestGoogleSignInRejectsUnsignedState(t *testing.T) {
	store := newFakeStore()
	svc := newGoogleService(t, store, &fakeGoogle{enabled: true, identity: verifiedIdentity()})

	_, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", "not-a-real-state", "ua", "")
	if !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("want ErrStateInvalid, got %v", err)
	}
}

// The PKCE verifier persisted at start is the one replayed at the exchange.
func TestGoogleSignInReplaysThePersistedPKCEVerifier(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	svc := newGoogleService(t, store, g)

	state := startAndState(t, svc, "")
	if g.lastAuthURLVerifier == "" {
		t.Fatal("AuthCodeURL was not given a PKCE verifier")
	}
	if _, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", state, "ua", ""); err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}
	if g.gotVerifier != g.lastAuthURLVerifier {
		t.Fatalf("exchange replayed %q but the challenge was built from %q", g.gotVerifier, g.lastAuthURLVerifier)
	}
}

// Signing in again resolves through the IDENTITY, not the email — so a Google
// address change still lands on the same account instead of minting a second one.
func TestGoogleSignInReusesIdentityAfterEmailChange(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	svc := newGoogleService(t, store, g)

	first, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, ""), "ua", "")
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}

	// Same Google account, new address.
	g.identity.Email = "dana.renamed@axomble.com"
	second, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, ""), "ua", "")
	if err != nil {
		t.Fatalf("second sign-in: %v", err)
	}
	if second.UserID != first.UserID {
		t.Fatalf("want the same user (%s), got %s — identity must key on sub, not email", first.UserID, second.UserID)
	}
	if len(store.workspaces) != 1 {
		t.Fatalf("want no second workspace, got %d", len(store.workspaces))
	}
}

// An existing password account for the same VERIFIED address is linked, not
// duplicated, and the user keeps their password.
func TestGoogleSignInLinksExistingAccount(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
	svc := newGoogleService(t, store, g)

	reg, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Axomble", Email: "newperson@axomble.com", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	sess, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, ""), "ua", "")
	if err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}
	if sess.UserID != reg.UserID {
		t.Fatalf("want the existing user %s, got %s", reg.UserID, sess.UserID)
	}
	if sess.WorkspaceID != reg.WorkspaceID {
		t.Fatalf("want their existing workspace %s, got %s", reg.WorkspaceID, sess.WorkspaceID)
	}
	row, ok := store.identities[identityKey(ProviderGoogle, "google-sub-1")]
	if !ok || row.UserID != reg.UserID {
		t.Fatal("want the google identity linked to the existing user")
	}
	if store.usersByID[reg.UserID].PasswordHash == nil {
		t.Fatal("linking must not strip the account's password")
	}
}

// email_verified=false refuses BOTH linking and signup. If it did not, anyone who
// could create a Google account asserting an address they don't control would be
// handed the Inroad account for it.
func TestGoogleSignInRefusesUnverifiedEmail(t *testing.T) {
	t.Run("refuses linking to an existing account", func(t *testing.T) {
		store := newFakeStore()
		id := verifiedIdentity()
		id.EmailVerified = false
		g := &fakeGoogle{enabled: true, identity: id}
		svc := newGoogleService(t, store, g)

		if _, err := svc.Register(context.Background(), RegisterInput{
			WorkspaceName: "Axomble", Email: "newperson@axomble.com", Password: "s3cret-pw",
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}

		_, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, ""), "ua", "")
		if !errors.Is(err, ErrProviderEmailUnverified) {
			t.Fatalf("want ErrProviderEmailUnverified, got %v", err)
		}
		if len(store.identities) != 0 {
			t.Fatal("an unverified email must link no identity")
		}
	})

	t.Run("refuses signup", func(t *testing.T) {
		store := newFakeStore()
		id := verifiedIdentity()
		id.EmailVerified = false
		svc := newGoogleService(t, store, &fakeGoogle{enabled: true, identity: id})

		_, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, ""), "ua", "")
		if !errors.Is(err, ErrProviderEmailUnverified) {
			t.Fatalf("want ErrProviderEmailUnverified, got %v", err)
		}
		if len(store.workspaces) != 0 || len(store.users) != 0 {
			t.Fatal("an unverified email must create nothing")
		}
	})
}

// seedInvite registers an inviting workspace and returns a pending invite's raw
// token for email at role.
func seedInvite(t *testing.T, store *fakeStore, svc *Service, email, role string) (uuid.UUID, string) {
	t.Helper()
	owner, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Inviter Inc", Email: "owner@inviter.test", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken: %v", err)
	}
	id := uuid.New()
	store.invites[id] = gen.WorkspaceInvite{
		ID: id, WorkspaceID: owner.WorkspaceID, Email: email, Role: gen.MemberRole(role),
		TokenHash: hash, Status: gen.InviteStatusPending,
		ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)), CreatedAt: pgxTimestamp(time.Now()),
	}
	return owner.WorkspaceID, raw
}

// An invited newcomer JOINS the inviting workspace rather than getting one of
// their own — whether they arrived with the invite link or not.
func TestGoogleSignInInvitedUserJoinsInsteadOfCreating(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withToken  bool
		wantWSName string
	}{
		{"invite token carried through the flow", true, "Inviter Inc"},
		{"invite discovered by address", false, "Inviter Inc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			g := &fakeGoogle{enabled: true, identity: verifiedIdentity()}
			svc := newGoogleService(t, store, g)

			wsID, raw := seedInvite(t, store, svc, "newperson@axomble.com", "member")
			token := ""
			if tc.withToken {
				token = raw
			}

			sess, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, token), "ua", "")
			if err != nil {
				t.Fatalf("CompleteGoogleSignIn: %v", err)
			}
			if sess.WorkspaceID != wsID {
				t.Fatalf("want to join the inviting workspace %s, got %s", wsID, sess.WorkspaceID)
			}
			if sess.Role != "member" {
				t.Fatalf("want the invite's role (member), got %q", sess.Role)
			}
			// Only the inviter's workspace exists: joining must not also create one.
			if len(store.workspaces) != 1 {
				t.Fatalf("want exactly 1 workspace, got %d", len(store.workspaces))
			}
			if store.usersByID[sess.UserID].PasswordHash != nil {
				t.Fatal("an invited federated user must have no password")
			}
			if _, ok := store.identities[identityKey(ProviderGoogle, "google-sub-1")]; !ok {
				t.Fatal("want the google identity linked to the joined user")
			}
		})
	}
}

// An invite addressed to somebody else cannot be redeemed by whoever happens to
// hold the link: the invite token is a bearer credential, so the flow requires
// the provider-verified address to BE the invited one.
func TestGoogleSignInRejectsInviteForAnotherAddress(t *testing.T) {
	store := newFakeStore()
	g := &fakeGoogle{enabled: true, identity: verifiedIdentity()} // newperson@axomble.com
	svc := newGoogleService(t, store, g)

	_, raw := seedInvite(t, store, svc, "someone.else@axomble.com", "admin")

	_, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", startAndState(t, svc, raw), "ua", "")
	if !errors.Is(err, ErrInviteNotForIdentity) {
		t.Fatalf("want ErrInviteNotForIdentity, got %v", err)
	}
	if len(store.workspaces) != 1 {
		t.Fatal("a mismatched invite must neither join nor create a workspace")
	}
}

// With no Google credentials configured the flow is simply off, at both halves.
func TestGoogleSignInDisabled(t *testing.T) {
	store := newFakeStore()
	svc := newGoogleService(t, store, &fakeGoogle{enabled: false})

	if _, err := svc.StartGoogleSignIn(context.Background(), StartGoogleSignInInput{}); !errors.Is(err, ErrGoogleDisabled) {
		t.Fatalf("start: want ErrGoogleDisabled, got %v", err)
	}
	if _, _, err := svc.CompleteGoogleSignIn(context.Background(), "code", "state", "ua", ""); !errors.Is(err, ErrGoogleDisabled) {
		t.Fatalf("callback: want ErrGoogleDisabled, got %v", err)
	}
}

// A Service built WITHOUT the option behaves the same way — nil provider, not a
// nil-pointer panic.
func TestGoogleSignInUnwiredReportsDisabled(t *testing.T) {
	svc := newTestService(newFakeStore())
	if _, err := svc.StartGoogleSignIn(context.Background(), StartGoogleSignInInput{}); !errors.Is(err, ErrGoogleDisabled) {
		t.Fatalf("want ErrGoogleDisabled, got %v", err)
	}
}

func TestDeriveWorkspaceName(t *testing.T) {
	tests := []struct {
		name                       string
		hostedDomain, given, email string
		want                       string
	}{
		{"hosted domain titleized", "axomble.com", "Dana", "dana@axomble.com", "Axomble"},
		{"multi-label domain takes the first", "mail.axomble.co.uk", "Dana", "d@x.com", "Mail"},
		{"personal account uses the first name", "", "Dana", "dana@gmail.com", "Dana's workspace"},
		{"no claims at all falls back to the local part", "", "", "dana.smith@gmail.com", "Dana.smith's workspace"},
		{"nothing usable", "", "", "", "My workspace"},
		{"whitespace-only claims are not usable", "  ", "  ", "", "My workspace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveWorkspaceName(tt.hostedDomain, tt.given, tt.email); got != tt.want {
				t.Fatalf("want %q, got %q", tt.want, got)
			}
		})
	}
}

// A derived name can never exceed what the API would accept for workspace_name.
func TestDeriveWorkspaceNameClampsToValidatorLimit(t *testing.T) {
	long := strings.Repeat("a", 500)
	got := DeriveWorkspaceName(long+".com", "", "")
	if len(got) > maxWorkspaceNameLen {
		t.Fatalf("derived name is %d bytes, over the %d limit", len(got), maxWorkspaceNameLen)
	}
}

// safeReturnTo is an open-redirect guard, so it is tested as an allowlist: only a
// plain same-origin path survives, and every shape that a browser might resolve to
// a different origin is dropped.
func TestSafeReturnTo(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain path", "/inbox", "/inbox"},
		{"path with query and fragment", "/inbox?tab=unread#first", "/inbox?tab=unread#first"},
		{"root", "/", "/"},
		{"empty", "", ""},
		{"absolute https url", "https://evil.example/steal", ""},
		{"absolute http url", "http://evil.example", ""},
		{"scheme relative", "//evil.example/steal", ""},
		{"backslash smuggling", `/\evil.example`, ""},
		{"backslash scheme relative", `\\evil.example`, ""},
		{"javascript scheme", "javascript:alert(1)", ""},
		{"relative without leading slash", "inbox", ""},
		{"newline header split", "/inbox\nLocation: https://evil.example", ""},
		{"carriage return", "/inbox\r\nSet-Cookie: x=1", ""},
		{"tab", "/inbox\tx", ""},
		{"trailing space", "/inbox ", ""},
		{"null byte", "/inbox\x00", ""},
		{"over length", "/" + strings.Repeat("a", 600), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := safeReturnTo(tt.in); got != tt.want {
				t.Fatalf("safeReturnTo(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
