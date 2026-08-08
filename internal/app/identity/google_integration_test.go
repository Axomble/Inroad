//go:build integration

package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These tests exercise the real Postgres transactions behind Google sign-in.
//
// The Google provider itself is mocked at the GoogleAuthenticator seam
// (fakeGoogle): the code exchange with accounts.google.com is NOT covered here
// and cannot be — there is no way to obtain a real authorization code in a test.
// Everything from the authorization code onward IS covered against a real
// database: the atomicity of signup, the invite path, the email_verified refusal,
// NULL-password login, single-use state, and onboarding.

// newGooglePool opens a migrated pool for the sign-in integration tests.
func newGooglePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// newGoogleIntegrationService wires the REAL store with a fake provider.
func newGoogleIntegrationService(t *testing.T, pool *pgxpool.Pool, g *fakeGoogle) *Service {
	t.Helper()
	return NewService(NewStore(pool), testRefreshTTL, &fakeSender{}, "https://app.example.test",
		time.Hour, time.Hour, time.Hour, WithGoogleSignIn(g, testJWTSecret))
}

// googleTestEmail keeps repeat runs from colliding on the users.email unique
// index (the test database is not reset between runs). Named distinctly from
// phase2's uniqueEmail, which is a different helper in the same package.
func googleTestEmail(prefix string) string {
	return prefix + "-" + uuid.NewString() + "@axomble.test"
}

func googleIdentityFor(email string) GoogleIdentity {
	return GoogleIdentity{
		Subject: "sub-" + uuid.NewString(), Email: email, EmailVerified: true,
		GivenName: "Dana", HostedDomain: "axomble.test",
	}
}

// startState runs the start half against the real store and extracts the state.
func startState(t *testing.T, svc *Service, inviteToken string) string {
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

// A Google signup lands workspace + user + owner membership + session, all four,
// in one transaction.
func TestIntegrationGoogleSignupIsAtomic(t *testing.T) {
	pool := newGooglePool(t)
	q := gen.New(pool)
	ctx := context.Background()

	email := googleTestEmail("signup")
	g := &fakeGoogle{enabled: true, identity: googleIdentityFor(email)}
	svc := newGoogleIntegrationService(t, pool, g)

	sess, _, err := svc.CompleteGoogleSignIn(ctx, "code", startState(t, svc, ""), "ua", "1.2.3.4")
	if err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}

	ws, err := q.GetWorkspace(ctx, sess.WorkspaceID)
	if err != nil {
		t.Fatalf("workspace row missing: %v", err)
	}
	if ws.Name != "Axomble" {
		t.Fatalf("want the workspace named from the hd claim, got %q", ws.Name)
	}
	if ws.OnboardingCompletedAt.Valid {
		t.Fatal("a fresh workspace must have onboarding_completed_at NULL")
	}
	if !sess.OnboardingCompletedAt.IsZero() {
		t.Fatalf("the session must report onboarding as pending, got %v", sess.OnboardingCompletedAt)
	}

	user, err := q.GetUserByID(ctx, sess.UserID)
	if err != nil {
		t.Fatalf("user row missing: %v", err)
	}
	if user.PasswordHash != nil {
		t.Fatal("a federated user must have password_hash NULL in Postgres")
	}
	if !user.EmailVerifiedAt.Valid {
		t.Fatal("want email_verified_at set from the provider claim")
	}
	member, err := q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: sess.WorkspaceID, UserID: sess.UserID})
	if err != nil {
		t.Fatalf("membership row missing: %v", err)
	}
	if member.Role != gen.MemberRoleOwner {
		t.Fatalf("want owner, got %q", member.Role)
	}
	if _, err := q.GetSessionByHash(ctx, auth.HashRefreshToken(sess.RawRefresh)); err != nil {
		t.Fatalf("session row missing: %v", err)
	}
	if _, err := q.GetUserIdentity(ctx, gen.GetUserIdentityParams{
		Provider: ProviderGoogle, ProviderSubject: g.identity.Subject,
	}); err != nil {
		t.Fatalf("identity row missing: %v", err)
	}
}

// If any step of the signup transaction fails, NOTHING lands — no orphan
// workspace, no orphan user, no orphan session.
//
// The transaction is driven directly rather than through the service, because the
// failure being forced (a provider identity claimed between the service's lookup
// and its insert) only happens for real when two callbacks race, and the service
// would otherwise short-circuit to sign-in before ever reaching the tx. The
// resolution order itself is covered by the unit tests; this is about atomicity.
func TestIntegrationFederatedSignupRollsBackWholly(t *testing.T) {
	pool := newGooglePool(t)
	q := gen.New(pool)
	store := NewStore(pool)
	ctx := context.Background()

	// An identity already claimed by an unrelated user.
	takenSubject := "sub-taken-" + uuid.NewString()
	squatter, err := q.CreateUser(ctx, gen.CreateUserParams{Email: googleTestEmail("squatter")})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := q.CreateUserIdentity(ctx, gen.CreateUserIdentityParams{
		UserID: squatter.ID, Provider: ProviderGoogle, ProviderSubject: takenSubject,
	}); err != nil {
		t.Fatalf("CreateUserIdentity: %v", err)
	}

	newEmail := googleTestEmail("rollback")
	raw, tokHash, err := auth.NewRefreshToken()
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	_, err = store.FederatedSignupTx(ctx, FederatedSignupTxParams{
		WorkspaceName: "Rollback Inc",
		Email:         newEmail,
		Identity:      IdentityLink{Provider: ProviderGoogle, Subject: takenSubject},
		SessionParams: gen.CreateSessionParams{
			TokenHash: tokHash, FamilyID: uuid.New(),
			ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)),
		},
	})
	if err == nil {
		t.Fatal("want the signup tx to fail on the duplicate provider identity")
	}

	// Every row the tx would have written is absent.
	if _, err := q.GetUserByEmail(ctx, newEmail); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("want no user row after rollback, got err=%v", err)
	}
	if _, err := q.GetSessionByHash(ctx, auth.HashRefreshToken(raw)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("want no session row after rollback, got err=%v", err)
	}
	var workspaces int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspaces WHERE name = 'Rollback Inc'`).Scan(&workspaces); err != nil {
		t.Fatalf("count workspaces: %v", err)
	}
	if workspaces != 0 {
		t.Fatalf("want no workspace row after rollback, got %d", workspaces)
	}
}

// A pending invite makes the newcomer JOIN the inviting workspace instead of
// creating one, at the invite's role, with no password.
func TestIntegrationGoogleInvitedUserJoins(t *testing.T) {
	pool := newGooglePool(t)
	q := gen.New(pool)
	ctx := context.Background()

	inviterEmail := googleTestEmail("inviter")
	inviteeEmail := googleTestEmail("invitee")

	g := &fakeGoogle{enabled: true, identity: googleIdentityFor(inviteeEmail)}
	svc := newGoogleIntegrationService(t, pool, g)

	owner, err := svc.Register(ctx, RegisterInput{
		WorkspaceName: "Inviter Inc", Email: inviterEmail, Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken: %v", err)
	}
	if _, err := q.CreateInvite(ctx, gen.CreateInviteParams{
		WorkspaceID: owner.WorkspaceID, Email: inviteeEmail, Role: gen.MemberRoleAdmin,
		TokenHash: hash, InvitedBy: owner.UserID, ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)),
	}); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}

	sess, _, err := svc.CompleteGoogleSignIn(ctx, "code", startState(t, svc, raw), "ua", "")
	if err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}
	if sess.WorkspaceID != owner.WorkspaceID {
		t.Fatalf("want to join workspace %s, got %s", owner.WorkspaceID, sess.WorkspaceID)
	}
	if sess.Role != "admin" {
		t.Fatalf("want the invite's role admin, got %q", sess.Role)
	}
	if len(sess.Memberships) != 1 {
		t.Fatalf("want exactly one membership (joined, not also created), got %d", len(sess.Memberships))
	}
	user, err := q.GetUserByEmail(ctx, inviteeEmail)
	if err != nil {
		t.Fatalf("invitee user row missing: %v", err)
	}
	if user.PasswordHash != nil {
		t.Fatal("an invited federated user must have password_hash NULL")
	}
}

// email_verified=false refuses to LINK an existing account — the check that stops
// an attacker registering a Google account asserting somebody else's address.
func TestIntegrationGoogleLinkRefusedWhenEmailUnverified(t *testing.T) {
	pool := newGooglePool(t)
	q := gen.New(pool)
	ctx := context.Background()

	email := googleTestEmail("unverified")
	id := googleIdentityFor(email)
	id.EmailVerified = false
	g := &fakeGoogle{enabled: true, identity: id}
	svc := newGoogleIntegrationService(t, pool, g)

	if _, err := svc.Register(ctx, RegisterInput{
		WorkspaceName: "Axomble", Email: email, Password: "s3cret-pw",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, _, err := svc.CompleteGoogleSignIn(ctx, "code", startState(t, svc, ""), "ua", "")
	if !errors.Is(err, ErrProviderEmailUnverified) {
		t.Fatalf("want ErrProviderEmailUnverified, got %v", err)
	}
	if _, err := q.GetUserIdentity(ctx, gen.GetUserIdentityParams{
		Provider: ProviderGoogle, ProviderSubject: id.Subject,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("want NO identity row to have been created, got err=%v", err)
	}
}

// A user whose password_hash is NULL in Postgres cannot authenticate with a
// password — not with the empty string, not with anything.
func TestIntegrationNullPasswordCannotAuthenticate(t *testing.T) {
	pool := newGooglePool(t)
	ctx := context.Background()

	email := googleTestEmail("nopassword")
	g := &fakeGoogle{enabled: true, identity: googleIdentityFor(email)}
	svc := newGoogleIntegrationService(t, pool, g)

	if _, _, err := svc.CompleteGoogleSignIn(ctx, "code", startState(t, svc, ""), "ua", ""); err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}
	for _, pw := range []string{"", " ", "password", "s3cret-pw"} {
		if _, err := svc.Authenticate(ctx, email, pw); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("password %q: want ErrInvalidCredentials, got %v", pw, err)
		}
	}
}

// The state nonce is single-use against the real UPDATE: a second callback with
// the same state is rejected and creates nothing.
func TestIntegrationLoginStateIsSingleUse(t *testing.T) {
	pool := newGooglePool(t)
	ctx := context.Background()

	email := googleTestEmail("replay")
	g := &fakeGoogle{enabled: true, identity: googleIdentityFor(email)}
	svc := newGoogleIntegrationService(t, pool, g)

	state := startState(t, svc, "")
	if _, _, err := svc.CompleteGoogleSignIn(ctx, "code", state, "ua", ""); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, _, err := svc.CompleteGoogleSignIn(ctx, "code", state, "ua", ""); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("want ErrStateInvalid on the second use, got %v", err)
	}
}

// An expired state row cannot be consumed either (the UPDATE's expires_at guard).
func TestIntegrationLoginStateRejectsExpiredRow(t *testing.T) {
	pool := newGooglePool(t)
	ctx := context.Background()
	store := NewStore(pool)

	nonce := "nonce-" + uuid.NewString()
	if err := store.CreateLoginState(ctx, CreateLoginStateParams{
		NonceHash: auth.HashToken(nonce), Purpose: "login", CodeVerifier: "verifier",
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateLoginState: %v", err)
	}
	if _, err := store.ConsumeLoginState(ctx, auth.HashToken(nonce), "login"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("want ErrStateInvalid for an expired row, got %v", err)
	}
}

// A state minted for one purpose cannot be consumed as another.
func TestIntegrationLoginStateRejectsWrongPurpose(t *testing.T) {
	pool := newGooglePool(t)
	ctx := context.Background()
	store := NewStore(pool)

	nonce := "nonce-" + uuid.NewString()
	if err := store.CreateLoginState(ctx, CreateLoginStateParams{
		NonceHash: auth.HashToken(nonce), Purpose: "login", CodeVerifier: "verifier",
		ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("CreateLoginState: %v", err)
	}
	if _, err := store.ConsumeLoginState(ctx, auth.HashToken(nonce), "mailbox_connect"); !errors.Is(err, ErrStateInvalid) {
		t.Fatalf("want ErrStateInvalid for a purpose mismatch, got %v", err)
	}
}

// Onboarding completion sets name + stamp atomically, and a replay neither renames
// nor re-stamps.
func TestIntegrationOnboardingCompletionIsIdempotent(t *testing.T) {
	pool := newGooglePool(t)
	q := gen.New(pool)
	ctx := context.Background()
	svc := newGoogleIntegrationService(t, pool, &fakeGoogle{})

	reg, err := svc.Register(ctx, RegisterInput{
		WorkspaceName: "Dana's workspace", Email: googleTestEmail("onboarding"), Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !reg.OnboardingCompletedAt.IsZero() {
		t.Fatalf("a fresh workspace must report onboarding pending, got %v", reg.OnboardingCompletedAt)
	}

	first, err := svc.CompleteOnboarding(ctx, reg.WorkspaceID, "Axomble Inc")
	if err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if first.Name != "Axomble Inc" || first.CompletedAt.IsZero() {
		t.Fatalf("want renamed + stamped, got %+v", first)
	}

	second, err := svc.CompleteOnboarding(ctx, reg.WorkspaceID, "Renamed By Replay")
	if err != nil {
		t.Fatalf("second complete: %v", err)
	}
	if second.Name != "Axomble Inc" {
		t.Fatalf("a replay must not rename; got %q", second.Name)
	}
	if !second.CompletedAt.Equal(first.CompletedAt) {
		t.Fatalf("a replay must keep the original timestamp: %v vs %v", second.CompletedAt, first.CompletedAt)
	}

	ws, err := q.GetWorkspace(ctx, reg.WorkspaceID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.Name != "Axomble Inc" || !ws.OnboardingCompletedAt.Valid {
		t.Fatalf("persisted row disagrees: name=%q stamped=%v", ws.Name, ws.OnboardingCompletedAt.Valid)
	}

	// Every subsequent auth response now reports it.
	sess, err := svc.Login(ctx, reg.Email, "s3cret-pw", "ua", "")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.OnboardingCompletedAt.IsZero() {
		t.Fatal("want a non-zero onboarding stamp on the session after completion")
	}
}

func TestIntegrationCompleteOnboardingUnknownWorkspace(t *testing.T) {
	pool := newGooglePool(t)
	svc := newGoogleIntegrationService(t, pool, &fakeGoogle{})
	if _, err := svc.CompleteOnboarding(context.Background(), uuid.New(), "Nope"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("want ErrWorkspaceNotFound, got %v", err)
	}
}

// A validated return_to round-trips through the real login-state row; an unsafe one
// is dropped at write time, so an off-origin destination is never even stored.
func TestIntegrationReturnToRoundTripsAndIsFiltered(t *testing.T) {
	pool := newGooglePool(t)
	ctx := context.Background()

	email := googleTestEmail("returnto")
	g := &fakeGoogle{enabled: true, identity: googleIdentityFor(email)}
	svc := newGoogleIntegrationService(t, pool, g)

	url, err := svc.StartGoogleSignIn(ctx, StartGoogleSignInInput{ReturnTo: "/inbox?tab=unread"})
	if err != nil {
		t.Fatalf("StartGoogleSignIn: %v", err)
	}
	_, state, ok := strings.Cut(url, "state=")
	if !ok {
		t.Fatalf("no state in %q", url)
	}
	_, returnTo, err := svc.CompleteGoogleSignIn(ctx, "code", state, "ua", "")
	if err != nil {
		t.Fatalf("CompleteGoogleSignIn: %v", err)
	}
	if returnTo != "/inbox?tab=unread" {
		t.Fatalf("want the stored return_to back, got %q", returnTo)
	}

	// An off-origin target never reaches the row.
	if _, err := svc.StartGoogleSignIn(ctx, StartGoogleSignInInput{ReturnTo: "https://evil.example/steal"}); err != nil {
		t.Fatalf("StartGoogleSignIn: %v", err)
	}
	var stored int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM oauth_login_states WHERE return_to IS NOT NULL AND return_to NOT LIKE '/%'`,
	).Scan(&stored); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != 0 {
		t.Fatalf("want no non-path return_to persisted, got %d", stored)
	}
}
