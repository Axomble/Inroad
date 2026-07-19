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

func (f *fakeStore) RegisterTx(ctx context.Context, wsName, email, hash string) (uuid.UUID, uuid.UUID, error) {
	if f.registerErr != nil {
		return uuid.Nil, uuid.Nil, f.registerErr
	}
	wsID := uuid.New()
	userID := uuid.New()
	user := gen.User{ID: userID, Email: email, PasswordHash: hash}
	f.users[email] = user
	f.usersByID[userID] = user
	member := gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: wsID, UserID: userID, Role: gen.MemberRoleOwner}
	f.memberByPair[[2]uuid.UUID{wsID, userID}] = member
	f.members[userID] = append(f.members[userID], gen.ListMembersByUserRow{
		ID: member.ID, WorkspaceID: wsID, UserID: userID, Role: gen.MemberRoleOwner, WorkspaceName: wsName,
	})
	return wsID, userID, nil
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

func (f *fakeStore) RevokeSession(ctx context.Context, id uuid.UUID) error {
	row, ok := f.sessions[id]
	if !ok {
		return errors.New("not found")
	}
	row.RevokedAt = pgxTimestamp(time.Now())
	f.sessions[id] = row
	return nil
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

func (f *fakeStore) RepointSessionWorkspace(ctx context.Context, id, wsID uuid.UUID) error {
	row, ok := f.sessions[id]
	if !ok {
		return errors.New("not found")
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
