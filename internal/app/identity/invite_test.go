package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// CreateInvite mirrors the real store's partial-unique-index behavior: a
// second pending invite for the same (workspace, email) pair fails with a
// *pgconn.PgError carrying the unique-violation code, exactly what
// isUniqueViolation checks for.
func (f *fakeStore) CreateInvite(ctx context.Context, arg gen.CreateInviteParams) (gen.WorkspaceInvite, error) {
	for _, inv := range f.invites {
		if inv.WorkspaceID == arg.WorkspaceID && inv.Email == arg.Email && inv.Status == gen.InviteStatusPending {
			return gen.WorkspaceInvite{}, &pgconn.PgError{Code: "23505"}
		}
	}
	inv := gen.WorkspaceInvite{
		ID: uuid.New(), WorkspaceID: arg.WorkspaceID, Email: arg.Email, Role: arg.Role,
		TokenHash: arg.TokenHash, InvitedBy: arg.InvitedBy, Status: gen.InviteStatusPending,
		ExpiresAt: arg.ExpiresAt, CreatedAt: pgxTimestamp(time.Now()),
	}
	f.invites[inv.ID] = inv
	return inv, nil
}

func (f *fakeStore) ListPendingInvites(ctx context.Context, wsID uuid.UUID) ([]gen.WorkspaceInvite, error) {
	var out []gen.WorkspaceInvite
	for _, inv := range f.invites {
		if inv.WorkspaceID == wsID && inv.Status == gen.InviteStatusPending {
			out = append(out, inv)
		}
	}
	return out, nil
}

// RevokeInvite mirrors the real UPDATE ... WHERE's affected-rows semantics: a
// missing, foreign-workspace, or already-resolved invite silently no-ops.
func (f *fakeStore) RevokeInvite(ctx context.Context, arg gen.RevokeInviteParams) error {
	inv, ok := f.invites[arg.ID]
	if !ok || inv.WorkspaceID != arg.WorkspaceID || inv.Status != gen.InviteStatusPending {
		return nil
	}
	inv.Status = gen.InviteStatusRevoked
	f.invites[arg.ID] = inv
	return nil
}

func (f *fakeStore) GetWorkspace(ctx context.Context, id uuid.UUID) (gen.Workspace, error) {
	ws, ok := f.workspaces[id]
	if !ok {
		return gen.Workspace{}, pgx.ErrNoRows
	}
	return ws, nil
}

// AcceptInviteTx mirrors the real store's single-transaction accept: token
// lookup by hash (pending, unexpired), resolve-or-create the user, upsert
// membership, mark accepted, mint a session - all against the fake's maps so
// tests exercise the same branches as Store.AcceptInviteTx.
func (f *fakeStore) AcceptInviteTx(ctx context.Context, arg AcceptInviteTxParams) (AcceptInviteTxResult, error) {
	hash := auth.HashToken(arg.RawToken)
	var invite gen.WorkspaceInvite
	found := false
	for _, inv := range f.invites {
		if string(inv.TokenHash) == string(hash) {
			invite, found = inv, true
			break
		}
	}
	if !found || invite.Status != gen.InviteStatusPending || time.Now().After(pgxTime(invite.ExpiresAt)) {
		return AcceptInviteTxResult{}, ErrTokenInvalid
	}

	user, existed := f.users[invite.Email]
	var userID uuid.UUID
	if !existed {
		if arg.PasswordHash == nil {
			return AcceptInviteTxResult{}, ErrPasswordRequired
		}
		userID = uuid.New()
		user = gen.User{ID: userID, Email: invite.Email, PasswordHash: *arg.PasswordHash}
	} else {
		userID = user.ID
	}
	user.EmailVerifiedAt = pgxTimestamp(time.Now())
	f.users[invite.Email] = user
	f.usersByID[userID] = user

	// Mirror the real store: add a membership, never mutate an existing
	// one's role. An owner/admin invited (accidentally or maliciously) at a
	// lower role and accepting must keep their existing role.
	role := invite.Role
	if existingMember, ok := f.memberByPair[[2]uuid.UUID{invite.WorkspaceID, userID}]; ok {
		role = existingMember.Role
	} else {
		member := gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: invite.WorkspaceID, UserID: userID, Role: invite.Role}
		f.memberByPair[[2]uuid.UUID{invite.WorkspaceID, userID}] = member
		f.members[userID] = append(f.members[userID], gen.ListMembersByUserRow{
			ID: member.ID, WorkspaceID: invite.WorkspaceID, UserID: userID, Role: invite.Role,
			WorkspaceName: f.workspaces[invite.WorkspaceID].Name,
		})
	}

	invite.Status = gen.InviteStatusAccepted
	invite.AcceptedAt = pgxTimestamp(time.Now())
	f.invites[invite.ID] = invite

	sp := arg.SessionParams
	sp.UserID = userID
	sp.WorkspaceID = invite.WorkspaceID
	sessionID := uuid.New()
	f.sessions[sessionID] = gen.Session{
		ID: sessionID, UserID: userID, WorkspaceID: invite.WorkspaceID,
		TokenHash: sp.TokenHash, FamilyID: sp.FamilyID, ExpiresAt: sp.ExpiresAt,
		UserAgent: sp.UserAgent, Ip: sp.Ip,
	}
	f.sessionsByHash[hashKey(sp.TokenHash)] = sessionID

	return AcceptInviteTxResult{
		WorkspaceID: invite.WorkspaceID, UserID: userID, Role: string(role), SessionID: sessionID,
	}, nil
}

// TestCreateInviteEmails drives the happy path: the invite is persisted
// pending and the sender receives a message with an /accept-invite?token=
// link.
func TestCreateInviteEmails(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	svc := NewService(store, time.Hour, sender, "https://app.example.test", time.Hour, time.Hour, time.Hour)

	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	invitedBy := uuid.New()

	inv, err := svc.CreateInvite(context.Background(), wsID, invitedBy, "newhire@acme.test", "member")
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if inv.Email != "newhire@acme.test" || inv.Status != "pending" {
		t.Fatalf("expected returned invite pending for newhire@acme.test, got %+v", inv)
	}
	if !strings.Contains(sender.last.TextBody, "https://app.example.test/accept-invite?token=") {
		t.Fatalf("expected accept-invite link in sent email body, got %q", sender.last.TextBody)
	}
	found := false
	for _, inv := range store.invites {
		if inv.WorkspaceID == wsID && inv.Email == "newhire@acme.test" && inv.Status == gen.InviteStatusPending {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a pending invite to be persisted")
	}
}

// TestCreateInviteDuplicatePendingReturnsErrInviteExists confirms a second
// invite to the same (workspace, email) pair while one is still pending maps
// the store's unique-violation to ErrInviteExists.
func TestCreateInviteDuplicatePendingReturnsErrInviteExists(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	invitedBy := uuid.New()

	if _, err := svc.CreateInvite(context.Background(), wsID, invitedBy, "newhire@acme.test", "member"); err != nil {
		t.Fatalf("first CreateInvite: %v", err)
	}
	_, err := svc.CreateInvite(context.Background(), wsID, invitedBy, "newhire@acme.test", "member")
	if !errors.Is(err, ErrInviteExists) {
		t.Fatalf("expected ErrInviteExists, got %v", err)
	}
}

// TestAcceptInviteExistingUserAddsMembership confirms that when the invited
// email already belongs to a user, no password is required: the membership
// is upserted at the invite's role, the invite is marked accepted, the
// user's email is (re)marked verified, and a session is returned.
func TestAcceptInviteExistingUserAddsMembership(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	existingID := uuid.New()
	store.usersByID[existingID] = gen.User{ID: existingID, Email: "existing@acme.test"}
	store.users["existing@acme.test"] = store.usersByID[existingID]

	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken: %v", err)
	}
	inviteID := uuid.New()
	store.invites[inviteID] = gen.WorkspaceInvite{
		ID: inviteID, WorkspaceID: wsID, Email: "existing@acme.test", Role: gen.MemberRoleAdmin,
		TokenHash: hash, Status: gen.InviteStatusPending, ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)),
	}

	sess, err := svc.AcceptInvite(context.Background(), raw, nil, "test-ua", "1.2.3.4")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if sess.UserID != existingID {
		t.Fatalf("expected session for existing user %s, got %s", existingID, sess.UserID)
	}
	if sess.WorkspaceID != wsID || sess.Role != "admin" {
		t.Fatalf("expected workspace %s / role admin, got %s / %s", wsID, sess.WorkspaceID, sess.Role)
	}
	if sess.RawRefresh == "" {
		t.Fatal("expected non-empty RawRefresh")
	}
	if row := store.sessions[sess.SessionID]; row.UserAgent == nil || *row.UserAgent != "test-ua" {
		t.Fatalf("expected session to record the caller's user agent, got %+v", row.UserAgent)
	}
	m, ok := store.memberByPair[[2]uuid.UUID{wsID, existingID}]
	if !ok || m.Role != gen.MemberRoleAdmin {
		t.Fatalf("expected membership at role admin, got %+v (ok=%v)", m, ok)
	}
	if store.invites[inviteID].Status != gen.InviteStatusAccepted {
		t.Fatalf("expected invite marked accepted, got %s", store.invites[inviteID].Status)
	}
	if !store.usersByID[existingID].EmailVerifiedAt.Valid {
		t.Fatal("expected existing user's email to be marked verified by the invite")
	}
}

// TestAcceptInviteNewUserCreatesAccount confirms that when the invited email
// has no existing account and a password is supplied, a new verified user is
// created, membership is upserted, and a session is returned.
func TestAcceptInviteNewUserCreatesAccount(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken: %v", err)
	}
	inviteID := uuid.New()
	store.invites[inviteID] = gen.WorkspaceInvite{
		ID: inviteID, WorkspaceID: wsID, Email: "newhire@acme.test", Role: gen.MemberRoleMember,
		TokenHash: hash, Status: gen.InviteStatusPending, ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)),
	}

	pw := "s3cret-pw-123"
	sess, err := svc.AcceptInvite(context.Background(), raw, &pw, "test-ua", "1.2.3.4")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if sess.WorkspaceID != wsID || sess.Role != "member" {
		t.Fatalf("expected workspace %s / role member, got %s / %s", wsID, sess.WorkspaceID, sess.Role)
	}
	newUser, ok := store.usersByID[sess.UserID]
	if !ok {
		t.Fatal("expected new user row to exist")
	}
	if newUser.Email != "newhire@acme.test" {
		t.Fatalf("expected email newhire@acme.test, got %q", newUser.Email)
	}
	if !newUser.EmailVerifiedAt.Valid {
		t.Fatal("expected new user's email_verified_at to be set")
	}
	if !auth.CheckPassword(newUser.PasswordHash, pw) {
		t.Fatal("expected new user's password hash to match the supplied password")
	}
	if _, ok := store.memberByPair[[2]uuid.UUID{wsID, sess.UserID}]; !ok {
		t.Fatal("expected membership to be created for the new user")
	}
}

// TestAcceptInviteNewUserRequiresPassword confirms a nil password for a
// not-yet-registered invited email is rejected with ErrPasswordRequired
// rather than silently creating a passwordless account.
func TestAcceptInviteNewUserRequiresPassword(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken: %v", err)
	}
	inviteID := uuid.New()
	store.invites[inviteID] = gen.WorkspaceInvite{
		ID: inviteID, WorkspaceID: wsID, Email: "newhire@acme.test", Role: gen.MemberRoleMember,
		TokenHash: hash, Status: gen.InviteStatusPending, ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)),
	}

	_, err = svc.AcceptInvite(context.Background(), raw, nil, "test-ua", "1.2.3.4")
	if !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("expected ErrPasswordRequired, got %v", err)
	}
	if _, ok := store.invites[inviteID]; !ok || store.invites[inviteID].Status != gen.InviteStatusPending {
		t.Fatal("expected invite to remain pending after a rejected accept attempt")
	}
}

// TestAcceptInviteInvalidToken confirms a bogus token is rejected with
// ErrTokenInvalid.
func TestAcceptInviteInvalidToken(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	_, err := svc.AcceptInvite(context.Background(), "not-a-real-token", nil, "test-ua", "1.2.3.4")
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expected ErrTokenInvalid, got %v", err)
	}
}

// TestListInvitesScopedToWorkspace confirms ListInvites only returns pending
// invites for the requested workspace, not another tenant's.
func TestListInvitesScopedToWorkspace(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	wsA, wsB := uuid.New(), uuid.New()
	store.workspaces[wsA] = gen.Workspace{ID: wsA, Name: "A"}
	store.workspaces[wsB] = gen.Workspace{ID: wsB, Name: "B"}
	invitedBy := uuid.New()

	if _, err := svc.CreateInvite(context.Background(), wsA, invitedBy, "a@acme.test", "member"); err != nil {
		t.Fatalf("CreateInvite A: %v", err)
	}
	if _, err := svc.CreateInvite(context.Background(), wsB, invitedBy, "b@acme.test", "member"); err != nil {
		t.Fatalf("CreateInvite B: %v", err)
	}

	invites, err := svc.ListInvites(context.Background(), wsA)
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 1 || invites[0].Email != "a@acme.test" {
		t.Fatalf("expected exactly the workspace-A invite, got %+v", invites)
	}
}

// TestRevokeInviteMarksRevoked confirms RevokeInvite flips a pending invite
// to revoked when scoped correctly.
func TestRevokeInviteMarksRevoked(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	invitedBy := uuid.New()
	if _, err := svc.CreateInvite(context.Background(), wsID, invitedBy, "a@acme.test", "member"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	var inviteID uuid.UUID
	for id := range store.invites {
		inviteID = id
	}

	if err := svc.RevokeInvite(context.Background(), wsID, inviteID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if store.invites[inviteID].Status != gen.InviteStatusRevoked {
		t.Fatalf("expected invite revoked, got %s", store.invites[inviteID].Status)
	}
}

// TestRevokeInviteCrossWorkspaceNoOp confirms RevokeInvite scoped to the
// wrong workspace silently no-ops rather than revoking an invite that
// belongs to a different tenant.
func TestRevokeInviteCrossWorkspaceNoOp(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	wsA, wsB := uuid.New(), uuid.New()
	store.workspaces[wsA] = gen.Workspace{ID: wsA, Name: "A"}
	store.workspaces[wsB] = gen.Workspace{ID: wsB, Name: "B"}
	invitedBy := uuid.New()
	if _, err := svc.CreateInvite(context.Background(), wsA, invitedBy, "a@acme.test", "member"); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	var inviteID uuid.UUID
	for id, inv := range store.invites {
		if inv.WorkspaceID == wsA {
			inviteID = id
		}
	}

	// wsB has no business revoking wsA's invite.
	if err := svc.RevokeInvite(context.Background(), wsB, inviteID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if store.invites[inviteID].Status != gen.InviteStatusPending {
		t.Fatalf("expected invite to remain pending after cross-workspace revoke attempt, got %s", store.invites[inviteID].Status)
	}
}

// TestAcceptInviteExistingMemberRoleUnchanged confirms accepting an invite
// never mutates an existing membership's role: an owner invited (accidentally
// or maliciously) at a lower role keeps their existing role after accepting.
func TestAcceptInviteExistingMemberRoleUnchanged(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)

	ownerID := uuid.New()
	store.usersByID[ownerID] = gen.User{ID: ownerID, Email: "owner@acme.test"}
	store.users["owner@acme.test"] = store.usersByID[ownerID]

	wsID := uuid.New()
	store.workspaces[wsID] = gen.Workspace{ID: wsID, Name: "Acme"}
	store.memberByPair[[2]uuid.UUID{wsID, ownerID}] = gen.WorkspaceMember{
		ID: uuid.New(), WorkspaceID: wsID, UserID: ownerID, Role: gen.MemberRoleOwner,
	}

	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken: %v", err)
	}
	inviteID := uuid.New()
	store.invites[inviteID] = gen.WorkspaceInvite{
		ID: inviteID, WorkspaceID: wsID, Email: "owner@acme.test", Role: gen.MemberRoleMember,
		TokenHash: hash, Status: gen.InviteStatusPending, ExpiresAt: pgxTimestamp(time.Now().Add(time.Hour)),
	}

	sess, err := svc.AcceptInvite(context.Background(), raw, nil, "test-ua", "1.2.3.4")
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if sess.Role != "owner" {
		t.Fatalf("expected the caller's existing role owner to be preserved, got %q", sess.Role)
	}
	m, ok := store.memberByPair[[2]uuid.UUID{wsID, ownerID}]
	if !ok || m.Role != gen.MemberRoleOwner {
		t.Fatalf("expected membership role to remain owner, got %+v (ok=%v)", m, ok)
	}
	if len(store.members[ownerID]) != 0 {
		t.Fatalf("expected no duplicate membership row to be created, got %d", len(store.members[ownerID]))
	}
}
