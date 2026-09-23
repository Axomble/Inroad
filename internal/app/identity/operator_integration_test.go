//go:build integration

package identity

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/audit"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// newOperatorTestStore migrates and connects a real Postgres-backed Store, the
// same construction cmd/inroadctl uses (identity.NewStore(pool)) — these tests
// exercise the operator surface against the real schema rather than a fake.
func newOperatorTestStore(t *testing.T) (*Store, *gen.Queries) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool), gen.New(pool)
}

// TestOperatorListWorkspacesOrdersNewestFirst confirms the real
// `ORDER BY created_at DESC` — the fake store cannot exercise ordering since
// it iterates a Go map.
func TestOperatorListWorkspacesOrdersNewestFirst(t *testing.T) {
	store, q := newOperatorTestStore(t)
	ctx := context.Background()

	first, err := q.CreateWorkspace(ctx, fmt.Sprintf("First-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("CreateWorkspace first: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // force a distinct created_at
	second, err := q.CreateWorkspace(ctx, fmt.Sprintf("Second-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("CreateWorkspace second: %v", err)
	}

	got, err := store.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	idx := map[uuid.UUID]int{}
	for i, ws := range got {
		idx[ws.ID] = i
	}
	if idx[second.ID] >= idx[first.ID] {
		t.Fatalf("expected the newer workspace %s before the older %s, got order %v", second.ID, first.ID, got)
	}
}

// TestOperatorListUsersIncludesCreatedUsers confirms ListUsers surfaces a
// freshly created user by id (contents, not merely count).
func TestOperatorListUsersIncludesCreatedUsers(t *testing.T) {
	store, q := newOperatorTestStore(t)
	ctx := context.Background()

	email := fmt.Sprintf("operator-list-%d@identity-it.test", time.Now().UnixNano())
	hash, err := auth.HashPassword("s3cret-pw-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	user, err := q.CreateUser(ctx, gen.CreateUserParams{Email: email, PasswordHash: &hash})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	got, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	found := false
	for _, u := range got {
		if u.ID == user.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected user %s in ListUsers, got %d rows", user.ID, len(got))
	}
}

// TestOperatorUpsertMemberRoleAddsThenUpdates confirms the ON CONFLICT upsert
// against the real unique constraint: first call inserts, second call on the
// same (workspace, user) pair updates the role in place rather than erroring
// or duplicating a row.
func TestOperatorUpsertMemberRoleAddsThenUpdates(t *testing.T) {
	store, q := newOperatorTestStore(t)
	ctx := context.Background()

	ws, err := q.CreateWorkspace(ctx, "Grant Role Co")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	hash, err := auth.HashPassword("s3cret-pw-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	email := fmt.Sprintf("grant-%d@identity-it.test", time.Now().UnixNano())
	user, err := q.CreateUser(ctx, gen.CreateUserParams{Email: email, PasswordHash: &hash})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	roleEvent := func(to string) audit.Event {
		ev := audit.New(ctx, ws.ID, audit.ActionMemberRoleChanged, "user", user.ID.String(), audit.Metadata{"to_role": to})
		ev.Actor = audit.SystemActor("inroadctl")
		return ev
	}
	m1, err := store.UpsertMemberRole(ctx, ws.ID, user.ID, gen.MemberRoleMember, roleEvent("member"))
	if err != nil {
		t.Fatalf("UpsertMemberRole insert: %v", err)
	}
	if m1.Role != gen.MemberRoleMember {
		t.Fatalf("expected role member, got %s", m1.Role)
	}

	m2, err := store.UpsertMemberRole(ctx, ws.ID, user.ID, gen.MemberRoleOwner, roleEvent("owner"))
	if err != nil {
		t.Fatalf("UpsertMemberRole update: %v", err)
	}
	// Each change landed its audit row in the same transaction, naming the
	// transition read inside it: none -> member, then member -> owner.
	rows, err := store.pool.Query(ctx, `
		SELECT metadata->>'from_role', metadata->>'to_role' FROM audit_events
		WHERE workspace_id = $1 AND action = 'member.role_changed' ORDER BY created_at, id`, ws.ID)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	var transitions []string
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		transitions = append(transitions, from+"->"+to)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("audit rows: %v", err)
	}
	if len(transitions) != 2 || transitions[0] != "none->member" || transitions[1] != "member->owner" {
		t.Fatalf("role_changed transitions = %v, want [none->member member->owner]", transitions)
	}
	if m2.ID != m1.ID {
		t.Fatalf("expected the SAME membership row to be updated (id %s), got a new row %s", m1.ID, m2.ID)
	}
	if m2.Role != gen.MemberRoleOwner {
		t.Fatalf("expected role owner after the second grant, got %s", m2.Role)
	}

	persisted, err := q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: ws.ID, UserID: user.ID})
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if persisted.Role != gen.MemberRoleOwner {
		t.Fatalf("expected the persisted role to read back as owner, got %s", persisted.Role)
	}
}

// TestOperatorUpsertMemberRoleUnknownWorkspaceFailsForeignKey confirms a
// foreign workspace id fails the FK rather than silently inserting an orphan
// row — the Service layer checks existence first (GrantRole), but the Store
// primitive itself must not be the thing that would let that check be skipped.
func TestOperatorUpsertMemberRoleUnknownWorkspaceFailsForeignKey(t *testing.T) {
	store, q := newOperatorTestStore(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("s3cret-pw-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	email := fmt.Sprintf("orphan-%d@identity-it.test", time.Now().UnixNano())
	user, err := q.CreateUser(ctx, gen.CreateUserParams{Email: email, PasswordHash: &hash})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	unknownWS := uuid.New()
	ev := audit.New(ctx, unknownWS, audit.ActionMemberRoleChanged, "user", user.ID.String(), nil)
	if _, err := store.UpsertMemberRole(ctx, unknownWS, user.ID, gen.MemberRoleOwner, ev); err == nil {
		t.Fatal("expected an error granting a role in an unknown workspace, got nil")
	}
}

// TestOperatorCreateMemberTxAddsUserToExistingWorkspace confirms CreateMemberTx
// persists both the user and the membership atomically against the real schema.
func TestOperatorCreateMemberTxAddsUserToExistingWorkspace(t *testing.T) {
	store, q := newOperatorTestStore(t)
	ctx := context.Background()

	ws, err := q.CreateWorkspace(ctx, "Create Member Co")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	hash, err := auth.HashPassword("s3cret-pw-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	email := fmt.Sprintf("bootstrap-%d@identity-it.test", time.Now().UnixNano())

	uid, err := store.CreateMemberTx(ctx, ws.ID, email, hash, gen.MemberRoleAdmin)
	if err != nil {
		t.Fatalf("CreateMemberTx: %v", err)
	}

	user, err := q.GetUserByID(ctx, uid)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if user.Email != email {
		t.Fatalf("expected email %q, got %q", email, user.Email)
	}
	member, err := q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: ws.ID, UserID: uid})
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if member.Role != gen.MemberRoleAdmin {
		t.Fatalf("expected role admin, got %s", member.Role)
	}
}

// TestOperatorSetPasswordTxRevokesRealSessions confirms SetPasswordTx, against
// the real schema, actually flips revoked_at on an existing session row — the
// property that makes it safe for `inroadctl set-password` to use on a
// possibly-compromised account.
func TestOperatorSetPasswordTxRevokesRealSessions(t *testing.T) {
	store, q := newOperatorTestStore(t)
	ctx := context.Background()

	ws, err := q.CreateWorkspace(ctx, "Set Password Co")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	oldHash, err := auth.HashPassword("old-password-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	email := fmt.Sprintf("setpw-%d@identity-it.test", time.Now().UnixNano())
	user, err := q.CreateUser(ctx, gen.CreateUserParams{Email: email, PasswordHash: &oldHash})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	// sessions_membership_fkey requires the (workspace, user) pair to already
	// be a member — a session cannot exist for a workspace the user never
	// joined.
	if _, err := q.CreateMember(ctx, gen.CreateMemberParams{WorkspaceID: ws.ID, UserID: user.ID, Role: gen.MemberRoleOwner}); err != nil {
		t.Fatalf("CreateMember: %v", err)
	}
	_, hash, err := auth.NewRefreshToken()
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	session, err := q.CreateSession(ctx, gen.CreateSessionParams{
		UserID: user.ID, WorkspaceID: ws.ID, TokenHash: hash, FamilyID: uuid.New(),
		ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	newHash, err := auth.HashPassword("new-password-456")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	revoked, err := store.SetPasswordTx(ctx, user.ID, newHash)
	if err != nil {
		t.Fatalf("SetPasswordTx: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != session.ID {
		t.Fatalf("expected session %s revoked, got %v", session.ID, revoked)
	}

	row, err := q.GetSessionAuthState(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSessionAuthState: %v", err)
	}
	if !row.RevokedAt.Valid {
		t.Fatal("expected the session's revoked_at to be set after SetPasswordTx")
	}

	updated, err := q.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if updated.PasswordHash == nil || !auth.CheckPassword(*updated.PasswordHash, "new-password-456") {
		t.Fatal("expected the persisted hash to verify against the new password")
	}
}

// TestOperatorGrantRoleServiceEndToEnd exercises the Service method (not just
// the Store) against the real database: GrantRole must add a membership for a
// user who was never a member of the workspace at all.
func TestOperatorGrantRoleServiceEndToEnd(t *testing.T) {
	store, q := newOperatorTestStore(t)
	svc := newTestService(store)
	ctx := context.Background()

	ws, err := q.CreateWorkspace(ctx, "End To End Co")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	hash, err := auth.HashPassword("s3cret-pw-123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	email := fmt.Sprintf("e2e-owner-%d@identity-it.test", time.Now().UnixNano())
	user, err := q.CreateUser(ctx, gen.CreateUserParams{Email: email, PasswordHash: &hash})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if _, err := q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: ws.ID, UserID: user.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expected no pre-existing membership, got %v", err)
	}

	m, err := svc.GrantRole(ctx, email, ws.ID, "owner")
	if err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	if m.Role != "owner" || m.WorkspaceID != ws.ID {
		t.Fatalf("expected owner on %s, got %+v", ws.ID, m)
	}
	persisted, err := q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: ws.ID, UserID: user.ID})
	if err != nil {
		t.Fatalf("GetMember: %v", err)
	}
	if persisted.Role != gen.MemberRoleOwner {
		t.Fatalf("expected persisted role owner, got %s", persisted.Role)
	}
}
