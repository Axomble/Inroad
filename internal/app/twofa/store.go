package twofa

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// PgStore is the sqlc-backed persistence for the twofa domain. It holds the pool
// directly for the few multi-statement operations that must be atomic (confirm +
// seed recovery codes, consume-challenge + mark-recovery-used, disable) — the
// same shape identity.Store uses for RegisterTx/ResetPasswordTx.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPgStore builds a PgStore over the given pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

// GetUserTOTP returns the user's TOTP row (pgx.ErrNoRows if none exists).
func (s *PgStore) GetUserTOTP(ctx context.Context, userID uuid.UUID) (gen.UserTotp, error) {
	return s.q.GetUserTOTP(ctx, userID)
}

// UpsertPendingTOTP stores a fresh unconfirmed secret, refusing to overwrite an
// already-confirmed row (returns 0 rows in that case — the service maps that to
// "already enrolled").
func (s *PgStore) UpsertPendingTOTP(ctx context.Context, userID uuid.UUID, ciphertext string) (int64, error) {
	return s.q.UpsertPendingTOTP(ctx, gen.UpsertPendingTOTPParams{UserID: userID, SecretCiphertext: ciphertext})
}

// GetUserEmail returns a user's email for the provisioning URI label. Reuses the
// identity domain's GetUserByID query rather than adding a new one.
func (s *PgStore) GetUserEmail(ctx context.Context, userID uuid.UUID) (string, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return "", err
	}
	return u.Email, nil
}

// ListUnusedRecoveryCodes returns the user's still-usable recovery codes (id +
// hash) so the service can constant-time-compare a presented code against each.
func (s *PgStore) ListUnusedRecoveryCodes(ctx context.Context, userID uuid.UUID) ([]gen.ListUnusedRecoveryCodesRow, error) {
	return s.q.ListUnusedRecoveryCodes(ctx, userID)
}

// CountUnusedRecoveryCodes reports how many single-use recovery codes remain.
func (s *PgStore) CountUnusedRecoveryCodes(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.q.CountUnusedRecoveryCodes(ctx, userID)
}

// ConfirmTOTPTx activates the pending secret and seeds the recovery codes in one
// transaction: either the factor becomes usable with its codes, or nothing lands.
// Returns the number of rows confirmed (0 if already confirmed / missing, in
// which case no codes are written).
func (s *PgStore) ConfirmTOTPTx(ctx context.Context, userID uuid.UUID, codeHashes []string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	n, err := qtx.ConfirmTOTP(ctx, userID)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil // already confirmed or no pending row: don't seed codes
	}
	// Clear any stale codes from a prior enrollment before seeding fresh ones.
	if err := qtx.DeleteRecoveryCodes(ctx, userID); err != nil {
		return 0, err
	}
	for _, h := range codeHashes {
		if err := qtx.CreateRecoveryCode(ctx, gen.CreateRecoveryCodeParams{UserID: userID, CodeHash: h}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

// DeleteTwoFA removes every 2FA artifact for a user (TOTP secret, recovery codes,
// any live challenges) in one transaction — the disable path.
func (s *PgStore) DeleteTwoFA(ctx context.Context, userID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	if err := qtx.DeleteRecoveryCodes(ctx, userID); err != nil {
		return err
	}
	if err := qtx.DeleteTOTP(ctx, userID); err != nil {
		return err
	}
	if err := qtx.DeleteChallengesForUser(ctx, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CreateChallenge persists a hashed, TTL'd login-gate challenge for the user.
func (s *PgStore) CreateChallenge(ctx context.Context, userID uuid.UUID, hash []byte, ip *netip.Addr, expiresAt time.Time) (uuid.UUID, error) {
	return s.q.CreateChallenge(ctx, gen.CreateChallengeParams{
		UserID:        userID,
		ChallengeHash: hash,
		Ip:            ip,
		ExpiresAt:     pgxTimestamp(expiresAt),
	})
}

// GetChallengeByHash looks up a challenge by its token hash.
func (s *PgStore) GetChallengeByHash(ctx context.Context, hash []byte) (gen.TwoFactorChallenge, error) {
	return s.q.GetChallengeByHash(ctx, hash)
}

// IncrementChallengeAttempts bumps the try counter and returns the new value so
// the service can enforce the per-challenge cap.
func (s *PgStore) IncrementChallengeAttempts(ctx context.Context, id uuid.UUID) (int32, error) {
	return s.q.IncrementChallengeAttempts(ctx, id)
}

// ConsumeChallenge marks a challenge consumed (single-use). Returns rows affected
// (0 if it was already consumed).
func (s *PgStore) ConsumeChallenge(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.ConsumeChallenge(ctx, id)
}

// CountRecentChallengesForIP counts challenges issued to ip since the given time
// (per-IP throttle).
func (s *PgStore) CountRecentChallengesForIP(ctx context.Context, ip *netip.Addr, since time.Time) (int64, error) {
	return s.q.CountRecentChallengesForIP(ctx, gen.CountRecentChallengesForIPParams{Ip: ip, CreatedAt: pgxTimestamp(since)})
}

// ConsumeChallengeAndUseRecoveryTx atomically consumes the challenge and, if a
// recovery code was used, marks that code used — so a successful verify burns
// exactly one challenge and at most one recovery code, with no window for either
// to be replayed. Returns false if the challenge was already consumed or the
// recovery code was already used (a lost race), which the service treats as an
// invalid challenge.
func (s *PgStore) ConsumeChallengeAndUseRecoveryTx(ctx context.Context, challengeID uuid.UUID, recoveryID *uuid.UUID) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)

	n, err := qtx.ConsumeChallenge(ctx, challengeID)
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil // already consumed concurrently
	}
	if recoveryID != nil {
		m, err := qtx.UseRecoveryCode(ctx, *recoveryID)
		if err != nil {
			return false, err
		}
		if m == 0 {
			return false, nil // recovery code already used concurrently
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
