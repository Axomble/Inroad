package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore is an in-memory implementation of storeIface for unit tests.
type fakeStore struct {
	users          map[string]gen.User                      // by email
	usersByID      map[uuid.UUID]gen.User                   // by id
	members        map[uuid.UUID][]gen.ListMembersByUserRow // by user id
	memberByPair   map[[2]uuid.UUID]gen.WorkspaceMember     // [wsID, userID] -> member
	sessions       map[uuid.UUID]gen.Session
	sessionsByHash map[string]uuid.UUID // hex(hash) -> session id

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

func (f *fakeStore) GetUserByEmail(ctx context.Context, email string) (gen.User, error) {
	u, ok := f.users[email]
	if !ok {
		return gen.User{}, errors.New("not found")
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
	svc := NewService(store, time.Hour)

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

	svc := NewService(store, time.Hour)
	_, err = svc.Login(context.Background(), "user@acme.test", "wrong-password", "ua", "1.2.3.4")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestRefreshRotatesAndRevokesOld(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, time.Hour)

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
	svc := NewService(store, time.Hour)

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
	svc := NewService(store, time.Hour)

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
	svc := NewService(store, time.Hour)

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
	svc := NewService(store, time.Hour)

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
