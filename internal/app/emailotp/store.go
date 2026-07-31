// Package emailotp owns passwordless email one-time-code login: request a code by
// email, exchange it for a first-factor authentication. It is a login sub-flow but
// a DEDICATED slice (its own Store/Service/Handler) so the OTP lifecycle is
// unit-testable against an in-memory fake with no DB, and so it never imports the
// identity or twofa domains — it reaches session-issuance and the 2FA gate only
// through the narrow seams wired at the composition root.
package emailotp

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the persistence seam the service depends on (dependency inversion):
// exactly the methods it uses, so unit tests inject an in-memory fake with no DB.
// *PgStore satisfies it.
type Store interface {
	// GetUserIDByEmail resolves an email to its user id, returning pgx.ErrNoRows
	// when no such user exists (the caller treats that as the anti-enumeration
	// no-op path).
	GetUserIDByEmail(ctx context.Context, email string) (uuid.UUID, error)
	// ReplaceActiveCode invalidates any prior unconsumed code for the user and
	// seeds a fresh one, atomically — so there is at most one active code per user.
	ReplaceActiveCode(ctx context.Context, userID uuid.UUID, codeHash string, maxAttempts int32, expiresAt time.Time) error
	// GetActiveCode returns the user's single active (unconsumed) code, or
	// pgx.ErrNoRows if none is live.
	GetActiveCode(ctx context.Context, userID uuid.UUID) (gen.GetActiveEmailOTPRow, error)
	// ClaimCodeAttempt atomically increments the code's attempt counter iff it is
	// still live (unconsumed) and under its cap, returning the new count. It
	// returns pgx.ErrNoRows when the code is already consumed or exhausted — a dead
	// code. The single-statement check+increment is the atomic cap that N
	// concurrent wrong guesses cannot exceed.
	ClaimCodeAttempt(ctx context.Context, id uuid.UUID) (int32, error)
	// ConsumeCode marks the code consumed (single-use), returning rows affected (0
	// on a lost race with a concurrent consume).
	ConsumeCode(ctx context.Context, id uuid.UUID) (int64, error)
}

// PgStore is the sqlc-backed persistence for the emailotp domain. It holds the
// pool directly for the one multi-statement operation that must be atomic (replace
// the active code) — the same shape identity.Store / twofa.PgStore use.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPgStore builds a PgStore over the given pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

// GetUserIDByEmail resolves an email to its user id. Reuses the identity domain's
// GetUserByEmail query (the users table is shared) rather than adding a new one.
func (s *PgStore) GetUserIDByEmail(ctx context.Context, email string) (uuid.UUID, error) {
	u, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		return uuid.Nil, err
	}
	return u.ID, nil
}

// ReplaceActiveCode deletes any still-live code for the user and inserts a fresh
// one in a single transaction: either the old code is gone and the new one exists,
// or nothing changes. Enforces "one active code per user" against a concurrent
// second start.
func (s *PgStore) ReplaceActiveCode(ctx context.Context, userID uuid.UUID, codeHash string, maxAttempts int32, expiresAt time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	if err := qtx.DeleteActiveEmailOTP(ctx, userID); err != nil {
		return err
	}
	if _, err := qtx.CreateEmailOTP(ctx, gen.CreateEmailOTPParams{
		UserID:      userID,
		CodeHash:    codeHash,
		MaxAttempts: maxAttempts,
		ExpiresAt:   pgxTimestamp(expiresAt),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetActiveCode returns the user's single active (unconsumed) code.
func (s *PgStore) GetActiveCode(ctx context.Context, userID uuid.UUID) (gen.GetActiveEmailOTPRow, error) {
	return s.q.GetActiveEmailOTP(ctx, userID)
}

// ClaimCodeAttempt atomically claims one wrong-guess slot (see the Store doc).
func (s *PgStore) ClaimCodeAttempt(ctx context.Context, id uuid.UUID) (int32, error) {
	return s.q.ClaimEmailOTPAttempt(ctx, id)
}

// ConsumeCode marks the code consumed (single-use).
func (s *PgStore) ConsumeCode(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.ConsumeEmailOTP(ctx, id)
}
