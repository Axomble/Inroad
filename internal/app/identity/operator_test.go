package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ListWorkspaces returns every fake workspace. Order is not modeled (the fake
// is a map); ordering is covered by the integration test against the real
// ORDER BY created_at DESC query.
func (f *fakeStore) ListWorkspaces(ctx context.Context) ([]gen.Workspace, error) {
	out := make([]gen.Workspace, 0, len(f.workspaces))
	for _, ws := range f.workspaces {
		out = append(out, ws)
	}
	return out, nil
}

// ListUsers returns every fake user, same ordering caveat as ListWorkspaces.
func (f *fakeStore) ListUsers(ctx context.Context) ([]gen.User, error) {
	out := make([]gen.User, 0, len(f.usersByID))
	for _, u := range f.usersByID {
		out = append(out, u)
	}
	return out, nil
}

// SetPasswordTx mirrors the real store's shape: overwrite the hash, then
// revoke every session — reusing the same fake helpers ResetPasswordTx's fake
// already relies on.
func (f *fakeStore) SetPasswordTx(ctx context.Context, userID uuid.UUID, newHash string) ([]uuid.UUID, error) {
	if err := f.UpdatePasswordHash(ctx, userID, newHash); err != nil {
		return nil, err
	}
	return f.RevokeAllForUser(ctx, userID)
}

// UpsertMemberRole mirrors the real ON CONFLICT upsert: add a membership if
// none exists for (wsID, userID), otherwise update its role in place — and
// keep the members[userID] list (ListMembersByUser's fake backing store) in
// sync either way, exactly as the real store's join would reflect.
func (f *fakeStore) UpsertMemberRole(ctx context.Context, wsID, userID uuid.UUID, role gen.MemberRole) (gen.WorkspaceMember, error) {
	key := [2]uuid.UUID{wsID, userID}
	m, existed := f.memberByPair[key]
	if !existed {
		m = gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: wsID, UserID: userID}
	}
	m.Role = role
	f.memberByPair[key] = m
	if !existed {
		f.members[userID] = append(f.members[userID], gen.ListMembersByUserRow{
			ID: m.ID, WorkspaceID: wsID, UserID: userID, Role: role, WorkspaceName: f.workspaces[wsID].Name,
		})
		return m, nil
	}
	for i, row := range f.members[userID] {
		if row.WorkspaceID == wsID {
			f.members[userID][i].Role = role
		}
	}
	return m, nil
}

// CreateMemberTx mirrors the real store: a duplicate email surfaces the same
// *pgconn.PgError{Code:"23505"} isUniqueViolation checks for, exactly like
// CreateInvite's fake does for the partial unique index.
func (f *fakeStore) CreateMemberTx(ctx context.Context, wsID uuid.UUID, email, passwordHash string, role gen.MemberRole) (uuid.UUID, error) {
	if _, exists := f.users[email]; exists {
		return uuid.Nil, &pgconn.PgError{Code: "23505"}
	}
	userID := uuid.New()
	user := gen.User{ID: userID, Email: email, PasswordHash: &passwordHash}
	f.users[email] = user
	f.usersByID[userID] = user
	member := gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: wsID, UserID: userID, Role: role}
	f.memberByPair[[2]uuid.UUID{wsID, userID}] = member
	f.members[userID] = append(f.members[userID], gen.ListMembersByUserRow{
		ID: member.ID, WorkspaceID: wsID, UserID: userID, Role: role, WorkspaceName: f.workspaces[wsID].Name,
	})
	return userID, nil
}

// TestListWorkspacesReturnsAll confirms the service is a plain pass-through to
// the store for the operator listing (`inroadctl workspaces`).
func TestListWorkspacesReturnsAll(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	wsA, wsB := uuid.New(), uuid.New()
	store.workspaces[wsA] = gen.Workspace{ID: wsA, Name: "A"}
	store.workspaces[wsB] = gen.Workspace{ID: wsB, Name: "B"}

	got, err := svc.ListWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 workspaces, got %d", len(got))
	}
}

// TestListUsersReturnsAll confirms the service is a plain pass-through to the
// store for the operator listing (`inroadctl users`).
func TestListUsersReturnsAll(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	idA, idB := uuid.New(), uuid.New()
	store.usersByID[idA] = gen.User{ID: idA, Email: "a@acme.test"}
	store.usersByID[idB] = gen.User{ID: idB, Email: "b@acme.test"}

	got, err := svc.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 users, got %d", len(got))
	}
}

// TestSetPasswordUpdatesHashAndRevokesSessions confirms `inroadctl
// set-password`'s primitive hashes with the real argon2id path, overwrites
// the stored hash, and revokes every existing session for the account — a
// forgotten-password recovery must not leave a possibly-compromised session
// alive.
func TestSetPasswordUpdatesHashAndRevokesSessions(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	uid := uuid.New()
	oldHash, err := auth.HashPassword("old-password-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	store.usersByID[uid] = gen.User{ID: uid, Email: "owner@acme.test", PasswordHash: &oldHash}
	store.users["owner@acme.test"] = store.usersByID[uid]

	sid := uuid.New()
	store.sessions[sid] = gen.Session{ID: sid, UserID: uid}

	revoked, err := svc.SetPassword(context.Background(), "owner@acme.test", "new-password-456")
	if err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != sid {
		t.Fatalf("expected session %s revoked, got %v", sid, revoked)
	}
	newHash := *store.usersByID[uid].PasswordHash
	if !auth.CheckPassword(newHash, "new-password-456") {
		t.Fatal("expected stored hash to verify against the new password")
	}
	if auth.CheckPassword(newHash, "old-password-123") {
		t.Fatal("expected the old password to no longer verify")
	}
}

// TestSetPasswordUnknownEmailReturnsErrUserNotFound confirms a typo'd email
// fails with a specific, actionable error rather than a raw pgx.ErrNoRows an
// operator has to decode.
func TestSetPasswordUnknownEmailReturnsErrUserNotFound(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	if _, err := svc.SetPassword(context.Background(), "nobody@acme.test", "new-password-456"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

// TestGrantRoleAddsNewMembership confirms granting a role to a user who is
// NOT yet a member of the workspace adds the membership — restoring an owner
// who was dropped from the workspace entirely, not merely demoted.
func TestGrantRoleAddsNewMembership(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	uid := uuid.New()
	store.usersByID[uid] = gen.User{ID: uid, Email: "owner@acme.test"}
	store.users["owner@acme.test"] = store.usersByID[uid]
	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}

	m, err := svc.GrantRole(context.Background(), "owner@acme.test", wsID, "owner")
	if err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	if m.Role != "owner" || m.WorkspaceID != wsID {
		t.Fatalf("expected owner role on workspace %s, got %+v", wsID, m)
	}
	got, ok := store.memberByPair[[2]uuid.UUID{wsID, uid}]
	if !ok || got.Role != gen.MemberRoleOwner {
		t.Fatalf("expected a persisted owner membership, got %+v (ok=%v)", got, ok)
	}
}

// TestGrantRoleUpdatesExistingMembership confirms granting a role to an
// EXISTING member overwrites their role — restoring an owner who was merely
// demoted, the other half of "restore a lost owner."
func TestGrantRoleUpdatesExistingMembership(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	uid := uuid.New()
	store.usersByID[uid] = gen.User{ID: uid, Email: "demoted@acme.test"}
	store.users["demoted@acme.test"] = store.usersByID[uid]
	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	store.memberByPair[[2]uuid.UUID{wsID, uid}] = gen.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: wsID, UserID: uid, Role: gen.MemberRoleMember,
	}

	m, err := svc.GrantRole(context.Background(), "demoted@acme.test", wsID, "owner")
	if err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	if m.Role != "owner" {
		t.Fatalf("expected role owner, got %q", m.Role)
	}
	got := store.memberByPair[[2]uuid.UUID{wsID, uid}]
	if got.Role != gen.MemberRoleOwner {
		t.Fatalf("expected the persisted membership role to become owner, got %q", got.Role)
	}
}

// TestGrantRoleUnknownEmailReturnsErrUserNotFound confirms the specific error
// for an unknown email, checked before the workspace lookup.
func TestGrantRoleUnknownEmailReturnsErrUserNotFound(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}

	if _, err := svc.GrantRole(context.Background(), "nobody@acme.test", wsID, "owner"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

// TestGrantRoleUnknownWorkspaceReturnsErrWorkspaceNotFound confirms the
// specific error for an unknown workspace id.
func TestGrantRoleUnknownWorkspaceReturnsErrWorkspaceNotFound(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	uid := uuid.New()
	store.usersByID[uid] = gen.User{ID: uid, Email: "owner@acme.test"}
	store.users["owner@acme.test"] = store.usersByID[uid]

	if _, err := svc.GrantRole(context.Background(), "owner@acme.test", uuid.New(), "owner"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("expected ErrWorkspaceNotFound, got %v", err)
	}
}

// TestGrantRoleInvalidRoleReturnsErrInvalidRole confirms a role outside the
// member_role enum is rejected in Go, before either lookup runs.
func TestGrantRoleInvalidRoleReturnsErrInvalidRole(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	if _, err := svc.GrantRole(context.Background(), "owner@acme.test", uuid.New(), "superadmin"); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("expected ErrInvalidRole, got %v", err)
	}
}

// TestCreateMemberAddsUserToExistingWorkspace confirms `inroadctl create-user
// --workspace` creates a new user (hashed with the real argon2id path) and
// adds them to the named EXISTING workspace at role, without touching
// sessions or minting one — contrast Register.
func TestCreateMemberAddsUserToExistingWorkspace(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}

	uid, err := svc.CreateMember(context.Background(), wsID, "newadmin@acme.test", "s3cret-pw-123", "admin")
	if err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	user, ok := store.usersByID[uid]
	if !ok || user.Email != "newadmin@acme.test" {
		t.Fatalf("expected a persisted user newadmin@acme.test, got %+v (ok=%v)", user, ok)
	}
	if user.PasswordHash == nil || !auth.CheckPassword(*user.PasswordHash, "s3cret-pw-123") {
		t.Fatal("expected the stored hash to verify against the supplied password")
	}
	m, ok := store.memberByPair[[2]uuid.UUID{wsID, uid}]
	if !ok || m.Role != gen.MemberRoleAdmin {
		t.Fatalf("expected an admin membership on %s, got %+v (ok=%v)", wsID, m, ok)
	}
	if len(store.sessions) != 0 {
		t.Fatalf("expected no session to be minted, got %d", len(store.sessions))
	}
}

// TestCreateMemberUnknownWorkspaceReturnsErrWorkspaceNotFound confirms
// bootstrapping into a workspace id that doesn't exist fails cleanly rather
// than as an opaque foreign-key violation.
func TestCreateMemberUnknownWorkspaceReturnsErrWorkspaceNotFound(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	if _, err := svc.CreateMember(context.Background(), uuid.New(), "new@acme.test", "s3cret-pw-123", "member"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("expected ErrWorkspaceNotFound, got %v", err)
	}
}

// TestCreateMemberInvalidRoleReturnsErrInvalidRole confirms role validation
// runs before any store call.
func TestCreateMemberInvalidRoleReturnsErrInvalidRole(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	if _, err := svc.CreateMember(context.Background(), uuid.New(), "new@acme.test", "s3cret-pw-123", "root"); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("expected ErrInvalidRole, got %v", err)
	}
}

// TestCreateMemberDuplicateEmailIsUniqueViolation confirms a second create-user
// for an email that already exists surfaces the same *pgconn.PgError
// isUniqueViolation checks for elsewhere in this package (handler.go), rather
// than a new, divergent error shape.
func TestCreateMemberDuplicateEmailIsUniqueViolation(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	store.users["taken@acme.test"] = gen.User{ID: uuid.New(), Email: "taken@acme.test"}

	_, err := svc.CreateMember(context.Background(), wsID, "taken@acme.test", "s3cret-pw-123", "member")
	if !isUniqueViolation(err) {
		t.Fatalf("expected a unique-violation error, got %v", err)
	}
}
