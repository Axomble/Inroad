// Package identity owns authentication: users, workspace membership, and sessions.
package identity

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store wraps the sqlc-generated queries for the identity domain (users,
// workspaces, workspace members, sessions) and adds the one multi-statement
// operation (RegisterTx) that must run atomically.
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewStore constructs a Store backed by the given connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

// RegisterTx creates a workspace, a user, and an owner membership linking
// them, all in a single transaction. It returns the new workspace and user
// IDs, or an error if any step fails (the transaction is rolled back).
func (s *Store) RegisterTx(ctx context.Context, wsName, email, hash string) (wsID, userID uuid.UUID, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	qtx := s.q.WithTx(tx)

	ws, err := qtx.CreateWorkspace(ctx, wsName)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	user, err := qtx.CreateUser(ctx, gen.CreateUserParams{
		Email:        email,
		PasswordHash: hash,
	})
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	if _, err = qtx.CreateMember(ctx, gen.CreateMemberParams{
		WorkspaceID: ws.ID,
		UserID:      user.ID,
		Role:        gen.MemberRoleOwner,
	}); err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, uuid.Nil, err
	}

	return ws.ID, user.ID, nil
}

// GetUserByEmail returns the user with the given email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (gen.User, error) {
	return s.q.GetUserByEmail(ctx, email)
}

// GetUserByID returns the user with the given ID.
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (gen.User, error) {
	return s.q.GetUserByID(ctx, id)
}

// ListMembersByUser returns every workspace membership (with workspace name)
// for the given user, most recently seen first.
func (s *Store) ListMembersByUser(ctx context.Context, userID uuid.UUID) ([]gen.ListMembersByUserRow, error) {
	return s.q.ListMembersByUser(ctx, userID)
}

// GetMember returns the membership linking a workspace and a user.
func (s *Store) GetMember(ctx context.Context, wsID, userID uuid.UUID) (gen.WorkspaceMember, error) {
	return s.q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: wsID, UserID: userID})
}

// TouchMemberLastSeen updates a membership's last_seen_at to now.
func (s *Store) TouchMemberLastSeen(ctx context.Context, wsID, userID uuid.UUID) error {
	return s.q.TouchMemberLastSeen(ctx, gen.TouchMemberLastSeenParams{WorkspaceID: wsID, UserID: userID})
}

// CreateSession persists a new session row.
func (s *Store) CreateSession(ctx context.Context, arg gen.CreateSessionParams) (gen.Session, error) {
	return s.q.CreateSession(ctx, arg)
}

// GetSessionByHash looks up a session by its token hash.
func (s *Store) GetSessionByHash(ctx context.Context, tokenHash []byte) (gen.Session, error) {
	return s.q.GetSessionByHash(ctx, tokenHash)
}

// RevokeSession marks a single session as revoked, returning the number of
// rows actually flipped (0 if the session was already revoked or doesn't
// exist, letting the caller detect a concurrent revoke).
func (s *Store) RevokeSession(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.RevokeSession(ctx, id)
}

// RevokeFamily marks every session in a refresh-token family as revoked.
func (s *Store) RevokeFamily(ctx context.Context, familyID uuid.UUID) error {
	return s.q.RevokeFamily(ctx, familyID)
}

// RevokeAllForUser marks every active session belonging to a user as revoked.
func (s *Store) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	return s.q.RevokeAllForUser(ctx, userID)
}

// RepointSessionWorkspace switches a session's active workspace (used when a
// user swaps workspace context without re-authenticating).
func (s *Store) RepointSessionWorkspace(ctx context.Context, id, wsID uuid.UUID) error {
	return s.q.RepointSessionWorkspace(ctx, gen.RepointSessionWorkspaceParams{ID: id, WorkspaceID: wsID})
}
