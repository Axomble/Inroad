//go:build integration

package identity

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// newPhase2TestService wires a Service backed by the same real Postgres pool
// newIdentityTestServer uses, but exercises the service layer directly rather
// than through HTTP: verify/reset/invite have multi-step side effects (a
// captured email, token consumption, tx atomicity) that are more directly
// asserted against Service return values and DB rows than by scraping HTTP
// cookies. sender is the fakeSender from service_test.go (untagged, so it's
// already compiled into this build) - it captures the last message sent,
// which is where the single-use tokens these tests need are extracted from.
func newPhase2TestService(t *testing.T) (*Service, *fakeSender, *gen.Queries) {
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

	sender := &fakeSender{}
	svc := NewService(NewStore(pool), testRefreshTTL, sender, "https://app.example.test", time.Hour, time.Hour, time.Hour)
	svc.dispatch = func(f func()) { f() } // deterministic: ForgotPassword's deferred work runs inline

	return svc, sender, gen.New(pool)
}

// extractToken pulls the single-use token out of the transactional-email
// link the notify templates embed (…?token=<raw>), the same value the
// handler layer reads from a query param in real use.
func extractToken(t *testing.T, body string) string {
	t.Helper()
	idx := strings.Index(body, "http")
	if idx < 0 {
		t.Fatalf("no link found in email body: %q", body)
	}
	rest := body[idx:]
	if end := strings.IndexAny(rest, "\n "); end >= 0 {
		rest = rest[:end]
	}
	u, err := url.Parse(rest)
	if err != nil {
		t.Fatalf("parse link %q: %v", rest, err)
	}
	tok := u.Query().Get("token")
	if tok == "" {
		t.Fatalf("no token query param in link %q", rest)
	}
	return tok
}

func uniqueEmail(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s-%d@phase2-it.test", prefix, time.Now().UnixNano())
}

// TestPhase2VerifyEmailFlow drives register -> capture the verify token from
// the fake sender -> VerifyEmail -> email_verified_at set, and confirms login
// still succeeds afterward (verification must never disturb credentials).
func TestPhase2VerifyEmailFlow(t *testing.T) {
	svc, sender, q := newPhase2TestService(t)
	ctx := context.Background()

	email := uniqueEmail(t, "verify")
	password := "s3cret-pw-longenough"

	sess, err := svc.Register(ctx, RegisterInput{WorkspaceName: "Acme Verify", Email: email, Password: password})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if !strings.Contains(sender.last.TextBody, "/verify-email?token=") {
		t.Fatalf("expected a verify-email link in the sent message, got %q", sender.last.TextBody)
	}
	token := extractToken(t, sender.last.TextBody)

	verified, err := svc.IsEmailVerified(ctx, sess.UserID)
	if err != nil {
		t.Fatalf("IsEmailVerified: %v", err)
	}
	if verified {
		t.Fatal("expected a freshly registered user to be unverified")
	}

	if err := svc.VerifyEmail(ctx, token); err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}

	verified, err = svc.IsEmailVerified(ctx, sess.UserID)
	if err != nil {
		t.Fatalf("IsEmailVerified after verify: %v", err)
	}
	if !verified {
		t.Fatal("expected email_verified_at to be set after VerifyEmail")
	}
	user, err := q.GetUserByID(ctx, sess.UserID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if !user.EmailVerifiedAt.Valid {
		t.Fatal("expected email_verified_at column to be set in the DB")
	}

	// A verify token is single-use: replaying it must fail.
	if err := svc.VerifyEmail(ctx, token); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid replaying a consumed verify token, got %v", err)
	}

	// Login still works after verification.
	if _, err := svc.Login(ctx, email, password, "ua", "127.0.0.1"); err != nil {
		t.Fatalf("Login after verify: %v", err)
	}
}

// TestPhase2ForgotResetFlow drives forgot -> capture the reset token -> reset
// -> the pre-reset refresh token is now revoked (reuse fails, whole family
// revoked), matching ResetPasswordTx's "revoke every session" contract.
func TestPhase2ForgotResetFlow(t *testing.T) {
	svc, sender, _ := newPhase2TestService(t)
	ctx := context.Background()

	email := uniqueEmail(t, "forgot")
	oldPassword := "s3cret-pw-longenough"
	newPassword := "a-brand-new-pw-longenough"

	sess, err := svc.Register(ctx, RegisterInput{WorkspaceName: "Acme Forgot", Email: email, Password: oldPassword})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	preResetRefresh := sess.RawRefresh

	if err := svc.ForgotPassword(ctx, email); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	if !strings.Contains(sender.last.TextBody, "/reset-password?token=") {
		t.Fatalf("expected a reset-password link in the sent message, got %q", sender.last.TextBody)
	}
	token := extractToken(t, sender.last.TextBody)

	if err := svc.ResetPassword(ctx, token, newPassword); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}

	// The pre-reset refresh token must now be dead: Refresh treats an
	// unknown/revoked/expired token as reuse and revokes the whole family.
	if _, err := svc.Refresh(ctx, preResetRefresh, "ua", "127.0.0.1"); !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("expected ErrRefreshInvalid refreshing with the pre-reset token, got %v", err)
	}

	// Old password no longer works; new password does.
	if _, err := svc.Login(ctx, email, oldPassword, "ua", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials logging in with the old password, got %v", err)
	}
	if _, err := svc.Login(ctx, email, newPassword, "ua", "127.0.0.1"); err != nil {
		t.Fatalf("Login with new password: %v", err)
	}
}

// TestPhase2InviteAcceptNewUser drives invite -> accept as a brand-new user
// (password supplied): a workspace_members row is created at the invite's
// role and the resulting account is email-verified (accepting proves inbox
// ownership) without a separate verify step.
func TestPhase2InviteAcceptNewUser(t *testing.T) {
	svc, sender, q := newPhase2TestService(t)
	ctx := context.Background()

	owner, err := svc.Register(ctx, RegisterInput{WorkspaceName: "Acme Invite", Email: uniqueEmail(t, "owner"), Password: "s3cret-pw-longenough"})
	if err != nil {
		t.Fatalf("Register owner: %v", err)
	}

	inviteEmail := uniqueEmail(t, "invitee")
	if _, err := svc.CreateInvite(ctx, owner.WorkspaceID, owner.UserID, inviteEmail, "member"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if !strings.Contains(sender.last.TextBody, "/accept-invite?token=") {
		t.Fatalf("expected an accept-invite link in the sent message, got %q", sender.last.TextBody)
	}
	token := extractToken(t, sender.last.TextBody)

	password := "invitee-pw-longenough"
	sess, err := svc.AcceptInvite(ctx, token, &password, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if sess.WorkspaceID != owner.WorkspaceID {
		t.Fatalf("expected session workspace %s, got %s", owner.WorkspaceID, sess.WorkspaceID)
	}
	if sess.Role != "member" {
		t.Fatalf("expected role member, got %q", sess.Role)
	}

	member, err := q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: owner.WorkspaceID, UserID: sess.UserID})
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if member.Role != gen.MemberRoleMember {
		t.Fatalf("expected workspace_members role member, got %q", member.Role)
	}

	verified, err := svc.IsEmailVerified(ctx, sess.UserID)
	if err != nil {
		t.Fatalf("IsEmailVerified: %v", err)
	}
	if !verified {
		t.Fatal("expected the accepted invitee's email to already be verified")
	}

	// The new account can log in with the password it was created with.
	if _, err := svc.Login(ctx, inviteEmail, password, "ua", "127.0.0.1"); err != nil {
		t.Fatalf("Login as invited user: %v", err)
	}
}

// TestPhase2InviteAcceptExistingUser drives invite -> accept as an EXISTING
// user (no password, resolved by email): a new membership is added, and a
// second invite at a different role into a workspace the user already
// belongs to leaves their existing role untouched (AcceptInviteTx never
// mutates an existing membership's role).
func TestPhase2InviteAcceptExistingUser(t *testing.T) {
	svc, sender, q := newPhase2TestService(t)
	ctx := context.Background()

	// The existing user: owns their own workspace already, so they have an
	// account and a verified-or-not email independent of the invites below.
	existingEmail := uniqueEmail(t, "existing")
	existing, err := svc.Register(ctx, RegisterInput{WorkspaceName: "Existing User's Own WS", Email: existingEmail, Password: "s3cret-pw-longenough"})
	if err != nil {
		t.Fatalf("Register existing user: %v", err)
	}

	// A second, unrelated workspace invites the existing user's email at
	// "member".
	inviter, err := svc.Register(ctx, RegisterInput{WorkspaceName: "Inviting WS", Email: uniqueEmail(t, "inviter"), Password: "s3cret-pw-longenough"})
	if err != nil {
		t.Fatalf("Register inviter: %v", err)
	}
	if _, err := svc.CreateInvite(ctx, inviter.WorkspaceID, inviter.UserID, existingEmail, "member"); err != nil {
		t.Fatalf("CreateInvite (member): %v", err)
	}
	tok1 := extractToken(t, sender.last.TextBody)

	sess1, err := svc.AcceptInvite(ctx, tok1, nil, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("AcceptInvite as existing user: %v", err)
	}
	if sess1.UserID != existing.UserID {
		t.Fatalf("expected accept to resolve to the existing account %s, got %s", existing.UserID, sess1.UserID)
	}
	if sess1.Role != "member" {
		t.Fatalf("expected role member, got %q", sess1.Role)
	}
	member, err := q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: inviter.WorkspaceID, UserID: existing.UserID})
	if err != nil {
		t.Fatalf("GetMember after first accept: %v", err)
	}
	if member.Role != gen.MemberRoleMember {
		t.Fatalf("expected membership added at role member, got %q", member.Role)
	}

	// A second invite into the SAME workspace at a different role ("admin").
	// Accepting it must not upgrade the existing membership.
	if _, err := svc.CreateInvite(ctx, inviter.WorkspaceID, inviter.UserID, existingEmail, "admin"); err != nil {
		t.Fatalf("CreateInvite (admin): %v", err)
	}
	tok2 := extractToken(t, sender.last.TextBody)

	sess2, err := svc.AcceptInvite(ctx, tok2, nil, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("AcceptInvite second time: %v", err)
	}
	if sess2.Role != "member" {
		t.Fatalf("expected existing role (member) to be left unchanged, got %q", sess2.Role)
	}
	member, err = q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: inviter.WorkspaceID, UserID: existing.UserID})
	if err != nil {
		t.Fatalf("GetMember after second accept: %v", err)
	}
	if member.Role != gen.MemberRoleMember {
		t.Fatalf("expected workspace_members role to remain member, got %q", member.Role)
	}

	// Exactly one membership row exists for (workspace, user) - no duplicate.
	mems, err := q.ListMembersByUser(ctx, existing.UserID)
	if err != nil {
		t.Fatalf("ListMembersByUser: %v", err)
	}
	count := 0
	for _, m := range mems {
		if m.WorkspaceID == inviter.WorkspaceID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 membership row for (workspace, user), got %d", count)
	}
}

// TestPhase2AcceptInviteIsIdempotent covers AcceptInviteTx's atomicity: once
// consumed, a second accept of the same token fails and no duplicate
// membership is created.
func TestPhase2AcceptInviteIsIdempotent(t *testing.T) {
	svc, sender, q := newPhase2TestService(t)
	ctx := context.Background()

	owner, err := svc.Register(ctx, RegisterInput{WorkspaceName: "Acme Idempotent", Email: uniqueEmail(t, "owner"), Password: "s3cret-pw-longenough"})
	if err != nil {
		t.Fatalf("Register owner: %v", err)
	}
	inviteEmail := uniqueEmail(t, "invitee")
	if _, err := svc.CreateInvite(ctx, owner.WorkspaceID, owner.UserID, inviteEmail, "member"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	token := extractToken(t, sender.last.TextBody)
	password := "invitee-pw-longenough"

	sess, err := svc.AcceptInvite(ctx, token, &password, "ua", "127.0.0.1")
	if err != nil {
		t.Fatalf("first AcceptInvite: %v", err)
	}

	// Replaying the same (now-consumed) token must fail.
	if _, err := svc.AcceptInvite(ctx, token, &password, "ua", "127.0.0.1"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid on a second accept of the same token, got %v", err)
	}

	// No duplicate membership was created by the failed replay.
	mems, err := q.ListMembersByUser(ctx, sess.UserID)
	if err != nil {
		t.Fatalf("ListMembersByUser: %v", err)
	}
	count := 0
	for _, m := range mems {
		if m.WorkspaceID == owner.WorkspaceID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 membership row, got %d", count)
	}
}

// TestPhase2RequireVerifiedGatedRoute exercises auth.RequireVerified against
// real Postgres-backed data: an unverified user's IsEmailVerified is false,
// so a route gated behind RequireVerified rejects them with 403; after
// VerifyEmail it lets them through - the same middleware stack
// campaign.Routes and mailbox.Routes wire up (see launch/connect).
func TestPhase2RequireVerifiedGatedRoute(t *testing.T) {
	svc, sender, _ := newPhase2TestService(t)
	ctx := context.Background()

	email := uniqueEmail(t, "gated")
	sess, err := svc.Register(ctx, RegisterInput{WorkspaceName: "Acme Gated", Email: email, Password: "s3cret-pw-longenough"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	token := extractToken(t, sender.last.TextBody)

	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	r.With(auth.RequireVerified(svc)).Get("/protected", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	access, err := auth.IssueToken(testJWTSecret, auth.Claims{
		UserID: sess.UserID.String(), WorkspaceID: sess.WorkspaceID.String(), Role: sess.Role, SessionID: sess.SessionID.String(),
	}, testAccessTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}

	get := func() *http.Response {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/protected", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+access)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	if resp := get(); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for an unverified user, got %d", resp.StatusCode)
	}

	if err := svc.VerifyEmail(ctx, token); err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}

	if resp := get(); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after verifying, got %d", resp.StatusCode)
	}
}
