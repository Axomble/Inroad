package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
)

// fakeSender is an in-memory notify.Sender for tests: it captures the last
// message handed to Send instead of delivering it anywhere.
type fakeSender struct {
	last notify.Message
	err  error
}

func (f *fakeSender) Send(ctx context.Context, m notify.Message) error {
	f.last = m
	return f.err
}

// newTestService builds a Service wired to a no-op fakeSender and short but
// non-zero TTLs, for tests that don't care about email-verification details.
// dispatch is overridden to run inline rather than in a goroutine, so tests
// asserting on ForgotPassword's deferred side effects (rate-limit check,
// token issuance, send) stay deterministic.
func newTestService(store storeIface) *Service {
	svc := NewService(store, time.Hour, &fakeSender{}, "https://app.example.test", time.Hour, time.Hour, time.Hour)
	svc.dispatch = func(f func()) { f() }
	return svc
}

// fakeStore is an in-memory implementation of storeIface for unit tests.
type fakeStore struct {
	users          map[string]gen.User                      // by email
	usersByID      map[uuid.UUID]gen.User                   // by id
	members        map[uuid.UUID][]gen.ListMembersByUserRow // by user id
	memberByPair   map[[2]uuid.UUID]gen.WorkspaceMember     // [wsID, userID] -> member
	sessions       map[uuid.UUID]gen.Session
	sessionsByHash map[string]uuid.UUID      // hex(hash) -> session id
	tokens         map[string]*fakeUserToken // hashKey(hash) -> token

	registerErr error
	nextWS      uuid.UUID
	nextUser    uuid.UUID

	// preRevokeSession, when set, runs at the top of RevokeSession before it
	// reads the session row. Tests use it to simulate a concurrent request
	// revoking the same session between this call's read and write, so the
	// TOCTOU reuse gate in Service.Refresh can be exercised deterministically.
	preRevokeSession func(id uuid.UUID)
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:          map[string]gen.User{},
		usersByID:      map[uuid.UUID]gen.User{},
		members:        map[uuid.UUID][]gen.ListMembersByUserRow{},
		memberByPair:   map[[2]uuid.UUID]gen.WorkspaceMember{},
		sessions:       map[uuid.UUID]gen.Session{},
		sessionsByHash: map[string]uuid.UUID{},
		tokens:         map[string]*fakeUserToken{},
	}
}

func (f *fakeStore) RegisterTx(ctx context.Context, arg RegisterTxParams) (RegisterTxResult, error) {
	if f.registerErr != nil {
		return RegisterTxResult{}, f.registerErr
	}
	wsID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	user := gen.User{ID: userID, Email: arg.Email, PasswordHash: arg.PasswordHash}
	f.users[arg.Email] = user
	f.usersByID[userID] = user
	member := gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: wsID, UserID: userID, Role: gen.MemberRoleOwner}
	f.memberByPair[[2]uuid.UUID{wsID, userID}] = member
	f.members[userID] = append(f.members[userID], gen.ListMembersByUserRow{
		ID: member.ID, WorkspaceID: wsID, UserID: userID, Role: gen.MemberRoleOwner, WorkspaceName: arg.WorkspaceName,
	})
	// The real store creates the session inside the same transaction; the fake
	// mirrors that so tests exercise the same shape.
	sp := arg.SessionParams
	sp.UserID = userID
	sp.WorkspaceID = wsID
	row := gen.Session{
		ID: sessionID, UserID: userID, WorkspaceID: wsID,
		TokenHash: sp.TokenHash, FamilyID: sp.FamilyID, ExpiresAt: sp.ExpiresAt,
		UserAgent: sp.UserAgent, Ip: sp.Ip,
	}
	f.sessions[sessionID] = row
	f.sessionsByHash[hashKey(sp.TokenHash)] = sessionID
	return RegisterTxResult{WorkspaceID: wsID, UserID: userID, SessionID: sessionID}, nil
}

// GetUserByEmail returns pgx.ErrNoRows on a miss, matching the real store's
// pass-through of pgx's "no rows" error - callers (ForgotPassword) branch on
// that specific error to distinguish "unknown email" from a genuine lookup
// failure.
func (f *fakeStore) GetUserByEmail(ctx context.Context, email string) (gen.User, error) {
	u, ok := f.users[email]
	if !ok {
		return gen.User{}, pgx.ErrNoRows
	}
	return u, nil
}

func (f *fakeStore) ListMembersByUser(ctx context.Context, userID uuid.UUID) ([]gen.ListMembersByUserRow, error) {
	return f.members[userID], nil
}

func (f *fakeStore) GetMember(ctx context.Context, wsID, userID uuid.UUID) (gen.WorkspaceMember, error) {
	m, ok := f.memberByPair[[2]uuid.UUID{wsID, userID}]
	if !ok {
		return gen.WorkspaceMember{}, errors.New("not a member")
	}
	return m, nil
}

func (f *fakeStore) TouchMemberLastSeen(ctx context.Context, wsID, userID uuid.UUID) error {
	return nil
}

func hashKey(h []byte) string {
	return string(h)
}

func (f *fakeStore) CreateSession(ctx context.Context, arg gen.CreateSessionParams) (gen.Session, error) {
	row := gen.Session{
		ID:          uuid.New(),
		UserID:      arg.UserID,
		WorkspaceID: arg.WorkspaceID,
		TokenHash:   arg.TokenHash,
		FamilyID:    arg.FamilyID,
		ExpiresAt:   arg.ExpiresAt,
		UserAgent:   arg.UserAgent,
		Ip:          arg.Ip,
	}
	f.sessions[row.ID] = row
	f.sessionsByHash[hashKey(arg.TokenHash)] = row.ID
	return row, nil
}

func (f *fakeStore) GetSessionByHash(ctx context.Context, hash []byte) (gen.Session, error) {
	id, ok := f.sessionsByHash[hashKey(hash)]
	if !ok {
		return gen.Session{}, errors.New("not found")
	}
	return f.sessions[id], nil
}

// RevokeSession mirrors the real store's :execrows semantics: it reports
// how many rows it actually flipped from not-revoked to revoked. A row
// that's already revoked (e.g. a concurrent caller won the race) yields 0
// rows affected rather than an error, matching Postgres's UPDATE ... WHERE
// revoked_at IS NULL behavior.
func (f *fakeStore) RevokeSession(ctx context.Context, id uuid.UUID) (int64, error) {
	if f.preRevokeSession != nil {
		f.preRevokeSession(id)
	}
	row, ok := f.sessions[id]
	if !ok {
		return 0, errors.New("not found")
	}
	if row.RevokedAt.Valid {
		return 0, nil
	}
	row.RevokedAt = pgxTimestamp(time.Now())
	f.sessions[id] = row
	return 1, nil
}

func (f *fakeStore) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	for id, row := range f.sessions {
		if row.FamilyID == familyID && !row.RevokedAt.Valid {
			row.RevokedAt = pgxTimestamp(time.Now())
			f.sessions[id] = row
		}
	}
	return nil
}

func (f *fakeStore) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	for id, row := range f.sessions {
		if row.UserID == userID && !row.RevokedAt.Valid {
			row.RevokedAt = pgxTimestamp(time.Now())
			f.sessions[id] = row
		}
	}
	return nil
}

func (f *fakeStore) SetEmailVerified(ctx context.Context, id uuid.UUID) error {
	u, ok := f.usersByID[id]
	if !ok {
		return errors.New("not found")
	}
	u.EmailVerifiedAt = pgxTimestamp(time.Now())
	f.usersByID[id] = u
	f.users[u.Email] = u
	return nil
}

// UpdatePasswordHash overwrites the stored user's password_hash, mirroring
// the real store's targeted UPDATE.
func (f *fakeStore) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	u, ok := f.usersByID[id]
	if !ok {
		return errors.New("not found")
	}
	u.PasswordHash = hash
	f.usersByID[id] = u
	f.users[u.Email] = u
	return nil
}

// ResetPasswordTx mirrors the real store's atomic reset transaction: it
// consumes the token, then overwrites the password hash and revokes every
// session for the resulting user id, so tests can assert on either outcome
// via the same fake maps used elsewhere.
func (f *fakeStore) ResetPasswordTx(ctx context.Context, rawToken, kind, newHash string) (uuid.UUID, error) {
	uid, err := f.ConsumeUserToken(ctx, rawToken, kind)
	if err != nil {
		return uuid.Nil, err
	}
	if err := f.UpdatePasswordHash(ctx, uid, newHash); err != nil {
		return uuid.Nil, err
	}
	if err := f.RevokeAllForUser(ctx, uid); err != nil {
		return uuid.Nil, err
	}
	return uid, nil
}

func (f *fakeStore) RepointSessionWorkspace(ctx context.Context, id, userID, wsID uuid.UUID) error {
	row, ok := f.sessions[id]
	if !ok {
		return ErrNotMember
	}
	// The real store's WHERE binds (id, user_id) together: mismatched pairs
	// yield 0 rows affected and surface as ErrNotMember. The fake mirrors
	// that so cross-tenant IDOR tests exercise the same path.
	if row.UserID != userID {
		return ErrNotMember
	}
	row.WorkspaceID = wsID
	f.sessions[id] = row
	return nil
}

func TestRegisterIssuesSession(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	sess, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Acme", Email: "owner@acme.test", Password: "s3cret-pw", UserAgent: "test-ua", IP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if sess.RawRefresh == "" {
		t.Fatal("expected non-empty RawRefresh")
	}
	if sess.Role != "owner" {
		t.Fatalf("expected role owner, got %q", sess.Role)
	}
	if sess.UserID == uuid.Nil || sess.WorkspaceID == uuid.Nil || sess.SessionID == uuid.Nil {
		t.Fatal("expected non-nil ids in session")
	}
	if len(sess.Memberships) != 1 {
		t.Fatalf("expected 1 membership, got %d", len(sess.Memberships))
	}
}

func TestLoginWrongPassword(t *testing.T) {
	store := newFakeStore()
	hash, err := auth.HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	userID := uuid.New()
	store.users["user@acme.test"] = gen.User{ID: userID, Email: "user@acme.test", PasswordHash: hash}

	svc := newTestService(store)
	_, err = svc.Login(context.Background(), "user@acme.test", "wrong-password", "ua", "1.2.3.4")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestRefreshRotatesAndRevokesOld(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	reg, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Acme", Email: "owner@acme.test", Password: "s3cret-pw", UserAgent: "ua", IP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	oldSessionID := reg.SessionID
	oldRaw := reg.RawRefresh

	refreshed, err := svc.Refresh(context.Background(), oldRaw, "ua2", "5.6.7.8")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.RawRefresh == oldRaw {
		t.Fatal("expected new raw refresh token to differ from old")
	}
	if refreshed.SessionID == oldSessionID {
		t.Fatal("expected new session id to differ from old")
	}

	oldRow, ok := store.sessions[oldSessionID]
	if !ok {
		t.Fatal("expected old session row to still exist")
	}
	if !oldRow.RevokedAt.Valid {
		t.Fatal("expected old session to be marked revoked")
	}
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	reg, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Acme", Email: "owner@acme.test", Password: "s3cret-pw", UserAgent: "ua", IP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	oldRaw := reg.RawRefresh

	// First refresh succeeds and rotates the family forward.
	if _, err := svc.Refresh(context.Background(), oldRaw, "ua", "1.2.3.4"); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	// Presenting the now-revoked old token again should be detected as reuse:
	// the whole family gets revoked, and the call errors.
	_, err = svc.Refresh(context.Background(), oldRaw, "ua", "1.2.3.4")
	if !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("expected ErrRefreshInvalid on reuse, got %v", err)
	}

	for _, row := range store.sessions {
		if row.UserID == reg.UserID && !row.RevokedAt.Valid {
			t.Fatalf("expected all sessions in family revoked after reuse detection, session %s still active", row.ID)
		}
	}
}

// TestRefreshConcurrentReuseRevokesFamily exercises the TOCTOU gate directly:
// even when GetSessionByHash sees a not-yet-revoked, not-expired row (so the
// early reuse check passes), a concurrent rotation of the exact same session
// can win the race and revoke it first. RevokeSession must then report 0
// rows affected, and Refresh must treat that as reuse - revoking the whole
// family and returning ErrRefreshInvalid - rather than proceeding to mint a
// successor session (which would fork the family and let both sides of the
// race keep working).
func TestRefreshConcurrentReuseRevokesFamily(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	reg, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Acme", Email: "owner@acme.test", Password: "s3cret-pw", UserAgent: "ua", IP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Simulate another request's Refresh call revoking this same session
	// between our GetSessionByHash read and our RevokeSession write.
	store.preRevokeSession = func(id uuid.UUID) {
		row := store.sessions[id]
		row.RevokedAt = pgxTimestamp(time.Now())
		store.sessions[id] = row
	}

	_, err = svc.Refresh(context.Background(), reg.RawRefresh, "ua", "1.2.3.4")
	if !errors.Is(err, ErrRefreshInvalid) {
		t.Fatalf("expected ErrRefreshInvalid on concurrent reuse, got %v", err)
	}

	for _, row := range store.sessions {
		if row.UserID == reg.UserID && !row.RevokedAt.Valid {
			t.Fatalf("expected all sessions in family revoked after concurrent-reuse detection, session %s still active", row.ID)
		}
	}
}

func TestSwitchWorkspaceNonMember(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	userID := uuid.New()
	sessionID := uuid.New()
	target := uuid.New() // no membership registered for this workspace

	_, _, err := svc.SwitchWorkspace(context.Background(), sessionID, userID, target)
	if !errors.Is(err, ErrNotMember) {
		t.Fatalf("expected ErrNotMember, got %v", err)
	}
}

// TestSwitchWorkspaceForeignSessionIDRejected drives the IDOR guard: even a
// caller who is a member of the target workspace cannot repoint a session
// row that belongs to somebody else. RepointSessionWorkspace's WHERE binds
// (id, user_id) together — a mismatched pair yields 0 rows affected and
// the service surfaces ErrNotMember. Without the user_id filter (the
// pre-fix behavior) a stolen or guessed session id would let an attacker
// steer another user's session to a workspace they controlled.
func TestSwitchWorkspaceForeignSessionIDRejected(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	// Register two users, each with their own session.
	victim, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Victim Inc", Email: "victim@example.test", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register victim: %v", err)
	}
	attacker, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Attacker Inc", Email: "attacker@example.test", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register attacker: %v", err)
	}

	// Give attacker membership in a third workspace they'd like to hijack the
	// victim's session into.
	target := uuid.New()
	store.memberByPair[[2]uuid.UUID{target, attacker.UserID}] = gen.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: target, UserID: attacker.UserID, Role: gen.MemberRoleMember,
	}

	// Attacker attempts to repoint victim.SessionID (which they somehow
	// guessed / stole) — attacker.UserID doesn't own it, so the store must
	// treat this as a non-membership event.
	_, _, err = svc.SwitchWorkspace(context.Background(), victim.SessionID, attacker.UserID, target)
	if !errors.Is(err, ErrNotMember) {
		t.Fatalf("expected ErrNotMember on foreign session repoint, got %v", err)
	}

	// The victim's session must still point at its original workspace.
	if store.sessions[victim.SessionID].WorkspaceID != victim.WorkspaceID {
		t.Fatal("victim session was silently repointed by attacker")
	}
}

// TestRegisterSendsVerifyEmail drives Register end-to-end and confirms it
// mints an email_verify token and hands the sender a message containing the
// verify link, while leaving the new user's email unverified.
func TestRegisterSendsVerifyEmail(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	svc := NewService(store, time.Hour, sender, "https://app.example.test", time.Hour, time.Hour, time.Hour)

	sess, err := svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Acme", Email: "owner@acme.test", Password: "s3cret-pw", UserAgent: "ua", IP: "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !strings.Contains(sender.last.TextBody, "https://app.example.test/verify-email?token=") {
		t.Fatalf("expected verify link in sent email body, got %q", sender.last.TextBody)
	}
	user, ok := store.usersByID[sess.UserID]
	if !ok {
		t.Fatal("expected user row to exist")
	}
	if user.EmailVerifiedAt.Valid {
		t.Fatal("expected email_verified_at to be null immediately after register")
	}
}

// TestVerifyEmailMarksVerified issues a token directly against the store
// (bypassing Register) and confirms VerifyEmail consumes it and flips the
// user's email_verified_at.
func TestVerifyEmailMarksVerified(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	userID := uuid.New()
	store.usersByID[userID] = gen.User{ID: userID, Email: "owner@acme.test"}
	store.users["owner@acme.test"] = store.usersByID[userID]

	raw, err := store.IssueUserToken(context.Background(), userID, "email_verify", time.Hour)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}

	if err := svc.VerifyEmail(context.Background(), raw); err != nil {
		t.Fatalf("VerifyEmail: %v", err)
	}
	if !store.usersByID[userID].EmailVerifiedAt.Valid {
		t.Fatal("expected email_verified_at to be set after VerifyEmail")
	}
}

// TestVerifyEmailInvalidTokenReturnsErrTokenInvalid confirms a bogus token is
// rejected without touching the user row.
func TestVerifyEmailInvalidTokenReturnsErrTokenInvalid(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	if err := svc.VerifyEmail(context.Background(), "not-a-real-token"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

// TestResendVerificationRateLimited confirms a second resend within 60s of
// the first is rejected with ErrRateLimited, closing off a spam vector.
func TestResendVerificationRateLimited(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	userID := uuid.New()
	store.usersByID[userID] = gen.User{ID: userID, Email: "owner@acme.test"}

	if err := svc.ResendVerification(context.Background(), userID); err != nil {
		t.Fatalf("first ResendVerification: %v", err)
	}
	if err := svc.ResendVerification(context.Background(), userID); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited on second call within 60s, got %v", err)
	}
}

// TestResendVerificationHourlyCap confirms the coarser outer bound: even
// once each individual token clears the 60s cooldown (backdated here to 10
// minutes ago so the cooldown check doesn't fire), a 6th resend within the
// last hour is still rejected once 5 have already been issued.
func TestResendVerificationHourlyCap(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	userID := uuid.New()
	store.usersByID[userID] = gen.User{ID: userID, Email: "owner@acme.test"}

	for i := 0; i < 5; i++ {
		raw, err := store.IssueUserToken(context.Background(), userID, "email_verify", time.Hour)
		if err != nil {
			t.Fatalf("IssueUserToken %d: %v", i, err)
		}
		store.tokens[hashKey(auth.HashToken(raw))].issuedAt = time.Now().Add(-10 * time.Minute)
	}

	if err := svc.ResendVerification(context.Background(), userID); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited on 6th resend within the hourly cap, got %v", err)
	}
}

// TestForgotPasswordUnknownEmailNoLeakNoSend confirms an email with no
// matching account returns nil (never an error the handler could map to a
// distinguishing status code) and never reaches the sender - the two
// observable signals (response, email sent) must both stay silent so a
// caller can't enumerate registered addresses.
func TestForgotPasswordUnknownEmailNoLeakNoSend(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	svc := NewService(store, time.Hour, sender, "https://app.example.test", time.Hour, time.Hour, time.Hour)
	svc.dispatch = func(f func()) { f() }

	if err := svc.ForgotPassword(context.Background(), "nobody@acme.test"); err != nil {
		t.Fatalf("ForgotPassword: expected nil error for unknown email, got %v", err)
	}
	if sender.last.TextBody != "" {
		t.Fatalf("expected no email sent for unknown address, got body %q", sender.last.TextBody)
	}
}

// TestForgotPasswordKnownSendsReset confirms a known email gets a reset link
// emailed via notify.ResetEmail.
func TestForgotPasswordKnownSendsReset(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	svc := NewService(store, time.Hour, sender, "https://app.example.test", time.Hour, time.Hour, time.Hour)
	svc.dispatch = func(f func()) { f() }

	userID := uuid.New()
	store.usersByID[userID] = gen.User{ID: userID, Email: "owner@acme.test"}
	store.users["owner@acme.test"] = store.usersByID[userID]

	if err := svc.ForgotPassword(context.Background(), "owner@acme.test"); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	if !strings.Contains(sender.last.TextBody, "https://app.example.test/reset-password?token=") {
		t.Fatalf("expected reset link in sent email body, got %q", sender.last.TextBody)
	}
}

// TestForgotPasswordRateLimited confirms a second request within the
// tokenRateLimited cooldown window is silently throttled: the call still
// returns nil (no leak) but the sender is not invoked again.
func TestForgotPasswordRateLimited(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	svc := NewService(store, time.Hour, sender, "https://app.example.test", time.Hour, time.Hour, time.Hour)
	svc.dispatch = func(f func()) { f() }

	userID := uuid.New()
	store.usersByID[userID] = gen.User{ID: userID, Email: "owner@acme.test"}
	store.users["owner@acme.test"] = store.usersByID[userID]

	if err := svc.ForgotPassword(context.Background(), "owner@acme.test"); err != nil {
		t.Fatalf("first ForgotPassword: %v", err)
	}
	sender.last = notify.Message{}

	if err := svc.ForgotPassword(context.Background(), "owner@acme.test"); err != nil {
		t.Fatalf("second ForgotPassword: expected nil (no leak) even when rate-limited, got %v", err)
	}
	if sender.last.TextBody != "" {
		t.Fatalf("expected no email sent on rate-limited request, got body %q", sender.last.TextBody)
	}
}

// TestForgotPasswordDefersSideEffectsToDispatcher drives the dispatcher seam
// directly: with dispatch overridden to queue the closure instead of running
// it, ForgotPassword must return before the rate-limit check / token issuance
// / send happen. This is what makes the known-email path cost the same
// wall-clock time as the unknown-email path from the caller's perspective -
// the expensive work happens after the response, not before it.
func TestForgotPasswordDefersSideEffectsToDispatcher(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	svc := NewService(store, time.Hour, sender, "https://app.example.test", time.Hour, time.Hour, time.Hour)

	var queued func()
	svc.dispatch = func(f func()) { queued = f } // queue, don't run

	userID := uuid.New()
	store.usersByID[userID] = gen.User{ID: userID, Email: "owner@acme.test"}
	store.users["owner@acme.test"] = store.usersByID[userID]

	if err := svc.ForgotPassword(context.Background(), "owner@acme.test"); err != nil {
		t.Fatalf("ForgotPassword: %v", err)
	}
	if sender.last.TextBody != "" {
		t.Fatal("expected no email sent before the dispatched closure runs")
	}
	if queued == nil {
		t.Fatal("expected ForgotPassword to hand a closure to dispatch")
	}

	queued() // now run the deferred side effects
	if !strings.Contains(sender.last.TextBody, "https://app.example.test/reset-password?token=") {
		t.Fatalf("expected reset link in sent email body after running the queued closure, got %q", sender.last.TextBody)
	}
}

// TestResetPasswordSetsHashAndRevokesSessions drives the happy path: a valid
// password_reset token consumes exactly once, the user's password_hash is
// overwritten, and every active session for that user is revoked - so a
// password reset can't be undermined by a still-live session from before the
// attacker (or the legitimate owner, recovering from a compromise) reset it.
func TestResetPasswordSetsHashAndRevokesSessions(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	userID := uuid.New()
	oldHash, err := auth.HashPassword("old-password-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	store.usersByID[userID] = gen.User{ID: userID, Email: "owner@acme.test", PasswordHash: oldHash}
	store.users["owner@acme.test"] = store.usersByID[userID]

	// An active session that must be revoked by ResetPassword.
	sessionID := uuid.New()
	store.sessions[sessionID] = gen.Session{ID: sessionID, UserID: userID, FamilyID: uuid.New()}

	raw, err := store.IssueUserToken(context.Background(), userID, "password_reset", time.Hour)
	if err != nil {
		t.Fatalf("IssueUserToken: %v", err)
	}

	if err := svc.ResetPassword(context.Background(), raw, "brand-new-password-456"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if !auth.CheckPassword(store.usersByID[userID].PasswordHash, "brand-new-password-456") {
		t.Fatal("expected password_hash to be updated to the new password")
	}
	if !store.sessions[sessionID].RevokedAt.Valid {
		t.Fatal("expected the user's existing session to be revoked after reset")
	}
}

// TestResetPasswordInvalidToken confirms a bogus/expired/already-consumed
// token is rejected with ErrTokenInvalid and never touches the password hash.
func TestResetPasswordInvalidToken(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	if err := svc.ResetPassword(context.Background(), "not-a-real-token", "brand-new-password-456"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}
