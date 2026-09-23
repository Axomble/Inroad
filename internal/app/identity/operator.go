package identity

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/audit"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrUserNotFound is returned by the operator-facing Service methods (backing
// `inroadctl set-password` / `grant-role`) when no user has the given email.
var ErrUserNotFound = errors.New("user not found")

// ErrInvalidRole is returned by GrantRole/CreateMember when role is not one of
// the member_role enum's values (owner/admin/member).
var ErrInvalidRole = errors.New("invalid role: must be owner, admin, or member")

// operatorStoreIface lists the Store methods the operator-only surface
// (list/recover/grant, backing cmd/inroadctl) adds on top of storeIface. Split
// out for the same reason as googleStoreIface: the seam each piece of the
// service depends on stays readable as its own list, not because it is
// injected separately.
type operatorStoreIface interface {
	ListWorkspaces(ctx context.Context) ([]gen.Workspace, error)
	ListUsers(ctx context.Context) ([]gen.User, error)
	SetPasswordTx(ctx context.Context, userID uuid.UUID, newHash string) ([]uuid.UUID, error)
	UpsertMemberRole(ctx context.Context, wsID, userID uuid.UUID, role gen.MemberRole, ev audit.Event) (gen.WorkspaceMember, error)
	CreateMemberTx(ctx context.Context, wsID uuid.UUID, email, passwordHash string, role gen.MemberRole) (uuid.UUID, error)
}

// validMemberRole reports whether role is one of the member_role enum's
// values. Checked in Go before it ever reaches Postgres so a typo produces
// ErrInvalidRole (a clean CLI message) rather than a raw enum-cast failure
// from the database.
func validMemberRole(role string) bool {
	switch gen.MemberRole(role) {
	case gen.MemberRoleOwner, gen.MemberRoleAdmin, gen.MemberRoleMember:
		return true
	default:
		return false
	}
}

// ListWorkspaces returns every workspace on the instance, newest first —
// `inroadctl workspaces`. Operator-only: there is no equivalent tenant-facing
// endpoint, deliberately (see Store.ListWorkspaces).
func (s *Service) ListWorkspaces(ctx context.Context) ([]gen.Workspace, error) {
	return s.store.ListWorkspaces(ctx)
}

// ListUsers returns every user on the instance, newest first — `inroadctl
// users`. The returned gen.User still carries PasswordHash (it is the sqlc
// persistence type); the CLI's printer must never render that field — see
// cmd/inroadctl's user-listing output.
func (s *Service) ListUsers(ctx context.Context) ([]gen.User, error) {
	return s.store.ListUsers(ctx)
}

// SetPassword hashes newPassword with the SAME argon2id path every other
// account-creation flow uses (auth.HashPassword) and overwrites email's
// password, revoking every one of their sessions in the same transaction —
// `inroadctl set-password`'s primitive for resetting a forgotten password with
// no working token flow. Returns ErrUserNotFound for an unknown email.
func (s *Service) SetPassword(ctx context.Context, email, newPassword string) ([]uuid.UUID, error) {
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	return s.store.SetPasswordTx(ctx, user.ID, hash)
}

// GrantRole adds email to workspace at role, or updates their existing
// membership if they already belong to it — `inroadctl grant-role`'s
// primitive for restoring a lost owner, whichever way the access was lost.
// Returns ErrUserNotFound / ErrWorkspaceNotFound / ErrInvalidRole for an
// unknown email, an unknown workspace, or a role outside the member_role enum,
// respectively — checked in that order so the error names exactly which input
// was wrong.
func (s *Service) GrantRole(ctx context.Context, email string, workspace uuid.UUID, role string) (Membership, error) {
	if !validMemberRole(role) {
		return Membership{}, ErrInvalidRole
	}
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Membership{}, ErrUserNotFound
		}
		return Membership{}, err
	}
	ws, err := s.store.GetWorkspace(ctx, workspace)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Membership{}, ErrWorkspaceNotFound
		}
		return Membership{}, err
	}
	// The operator CLI is the only caller and runs with no principal, so the
	// actor names the tool. The store adds from_role inside its transaction.
	ev := audit.New(ctx, workspace, audit.ActionMemberRoleChanged, "user", user.ID.String(), audit.Metadata{"email": user.Email, "to_role": role})
	ev.Actor = audit.SystemActor("inroadctl")
	m, err := s.store.UpsertMemberRole(ctx, workspace, user.ID, gen.MemberRole(role), ev)
	if err != nil {
		return Membership{}, err
	}
	return Membership{
		WorkspaceID: workspace, WorkspaceName: ws.Name, Role: string(m.Role),
		OnboardingCompletedAt: pgxTime(ws.OnboardingCompletedAt),
	}, nil
}

// CreateMember creates a brand-new user and adds them to an EXISTING workspace
// at role — `inroadctl create-user --workspace`'s primitive for bootstrapping
// a user into an instance that already has one, without minting a session or
// sending a verification email (contrast Register, which always creates a
// NEW workspace). Returns ErrInvalidRole for a role outside the member_role
// enum, ErrWorkspaceNotFound for an unknown workspace, and passes through the
// store's unique-violation on a duplicate email unmodified — the same shape
// Register already leaves for its caller to map (see handler.go's
// isUniqueViolation), so callers of both paths check the same way.
func (s *Service) CreateMember(ctx context.Context, workspace uuid.UUID, email, password, role string) (uuid.UUID, error) {
	if !validMemberRole(role) {
		return uuid.Nil, ErrInvalidRole
	}
	if _, err := s.store.GetWorkspace(ctx, workspace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrWorkspaceNotFound
		}
		return uuid.Nil, err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return uuid.Nil, err
	}
	return s.store.CreateMemberTx(ctx, workspace, email, hash, gen.MemberRole(role))
}
