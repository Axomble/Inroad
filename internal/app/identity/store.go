// Package identity owns authentication: users, workspace membership, and sessions.
package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrTokenInvalid is returned by ConsumeUserToken when the presented raw
// token doesn't match a stored hash for the given kind, or the matching row
// is already consumed or expired.
var ErrTokenInvalid = errors.New("token invalid or expired")

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

// RegisterTx creates a workspace, an owner user, membership, AND the first
// refresh-token session for that user — all in a single database
// transaction. Either every row lands or none does; a partial register can
// no longer leave a user with no workspace or a workspace with no session.
//
// Returns the new workspace id, user id, and session id. The session row is
// built from arg.SessionParams; the caller minted the token hash and family
// id (see identity.Service.Register).
func (s *Store) RegisterTx(ctx context.Context, arg RegisterTxParams) (RegisterTxResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RegisterTxResult{}, err
	}
	defer tx.Rollback(ctx)

	qtx := s.q.WithTx(tx)

	ws, err := qtx.CreateWorkspace(ctx, arg.WorkspaceName)
	if err != nil {
		return RegisterTxResult{}, err
	}

	user, err := qtx.CreateUser(ctx, gen.CreateUserParams{
		Email:        arg.Email,
		PasswordHash: arg.PasswordHash,
	})
	if err != nil {
		return RegisterTxResult{}, err
	}

	if _, err = qtx.CreateMember(ctx, gen.CreateMemberParams{
		WorkspaceID: ws.ID,
		UserID:      user.ID,
		Role:        gen.MemberRoleOwner,
	}); err != nil {
		return RegisterTxResult{}, err
	}

	// Session is created inside the same tx: if any earlier step (or the
	// commit itself) fails, no session row lingers for a user that isn't
	// there. UserID/WorkspaceID come from the just-inserted rows above.
	sp := arg.SessionParams
	sp.UserID = user.ID
	sp.WorkspaceID = ws.ID
	session, err := qtx.CreateSession(ctx, sp)
	if err != nil {
		return RegisterTxResult{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return RegisterTxResult{}, err
	}

	return RegisterTxResult{
		WorkspaceID: ws.ID,
		UserID:      user.ID,
		SessionID:   session.ID,
	}, nil
}

// RegisterTxParams carries the inputs RegisterTx needs. SessionParams
// carries the token hash, family id, expires_at, and client metadata for
// the initial session row — UserID and WorkspaceID are ignored here
// (RegisterTx fills them in from the rows it just inserted).
type RegisterTxParams struct {
	WorkspaceName string
	Email         string
	PasswordHash  string
	SessionParams gen.CreateSessionParams
}

// RegisterTxResult is the tuple of ids the caller needs to keep going
// (issue an access token, set cookies, load memberships).
type RegisterTxResult struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	SessionID   uuid.UUID
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

// IssueUserToken mints a new opaque single-use token of the given kind
// (email verify, password reset, ...) for userID, persisting only its
// SHA-256 hash with an expiry of ttl from now. Returns the raw token for the
// caller to embed in a link/email; the raw value is never stored.
func (s *Store) IssueUserToken(ctx context.Context, userID uuid.UUID, kind string, ttl time.Duration) (string, error) {
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return "", err
	}
	_, err = s.q.CreateUserToken(ctx, gen.CreateUserTokenParams{
		UserID:    userID,
		Kind:      gen.UserTokenKind(kind),
		TokenHash: hash,
		ExpiresAt: pgxTimestamp(time.Now().Add(ttl)),
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

// ConsumeUserToken looks up a user token by the hash of raw and kind, and
// atomically marks it consumed (single-use). Returns ErrTokenInvalid if no
// matching, unconsumed, unexpired row exists — a wrong token, a kind
// mismatch, a replay, or an expired token all look identical to the caller.
func (s *Store) ConsumeUserToken(ctx context.Context, raw, kind string) (uuid.UUID, error) {
	uid, err := s.q.ConsumeUserToken(ctx, gen.ConsumeUserTokenParams{
		TokenHash: auth.HashToken(raw),
		Kind:      gen.UserTokenKind(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrTokenInvalid
	}
	if err != nil {
		return uuid.Nil, err
	}
	return uid, nil
}

// CountRecentUserTokens returns how many tokens of kind have been issued to
// userID since the given time, for rate-limiting repeated issuance (e.g.
// password-reset requests).
func (s *Store) CountRecentUserTokens(ctx context.Context, userID uuid.UUID, kind string, since time.Time) (int64, error) {
	return s.q.CountRecentUserTokens(ctx, gen.CountRecentUserTokensParams{
		UserID:    userID,
		Kind:      gen.UserTokenKind(kind),
		CreatedAt: pgxTimestamp(since),
	})
}

// SetEmailVerified marks a user's email as verified (sets email_verified_at
// to now).
func (s *Store) SetEmailVerified(ctx context.Context, id uuid.UUID) error {
	return s.q.SetEmailVerified(ctx, id)
}

// UpdatePasswordHash overwrites a user's password_hash (used by password
// reset).
func (s *Store) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	return s.q.UpdatePasswordHash(ctx, gen.UpdatePasswordHashParams{ID: id, PasswordHash: hash})
}

// ResetPasswordTx atomically consumes a password_reset token, overwrites the
// owning user's password_hash, and revokes every one of their sessions - all
// in a single transaction, so a crash between steps can never leave the hash
// updated with old sessions still live (or the reverse). Mirrors RegisterTx's
// pattern of running several statements as one qtx-scoped unit.
func (s *Store) ResetPasswordTx(ctx context.Context, rawToken, kind, newHash string) (uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)

	qtx := s.q.WithTx(tx)

	uid, err := qtx.ConsumeUserToken(ctx, gen.ConsumeUserTokenParams{
		TokenHash: auth.HashToken(rawToken),
		Kind:      gen.UserTokenKind(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrTokenInvalid
	}
	if err != nil {
		return uuid.Nil, err
	}

	if err := qtx.UpdatePasswordHash(ctx, gen.UpdatePasswordHashParams{ID: uid, PasswordHash: newHash}); err != nil {
		return uuid.Nil, err
	}
	if err := qtx.RevokeAllForUser(ctx, uid); err != nil {
		return uuid.Nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return uid, nil
}

// RepointSessionWorkspace switches a session's active workspace (used when a
// user swaps workspace context without re-authenticating). The userID is
// checked in the WHERE clause so callers can only ever repoint their own
// sessions; a 0-row result means the (session, user) pair didn't match.
func (s *Store) RepointSessionWorkspace(ctx context.Context, id, userID, wsID uuid.UUID) error {
	n, err := s.q.RepointSessionWorkspace(ctx, gen.RepointSessionWorkspaceParams{
		ID: id, WorkspaceID: wsID, UserID: userID,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotMember
	}
	return nil
}
