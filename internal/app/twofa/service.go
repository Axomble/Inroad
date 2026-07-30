package twofa

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	// ErrAlreadyEnrolled is returned when enrolling/confirming a user who already
	// has a confirmed second factor.
	ErrAlreadyEnrolled = errors.New("two-factor is already enabled")
	// ErrNotEnrolled is returned when confirming/disabling a user who has no
	// pending or confirmed factor.
	ErrNotEnrolled = errors.New("two-factor is not enabled")
	// ErrBadCode is returned when a presented TOTP or recovery code does not
	// verify (wrong code — the challenge, if any, still has tries left).
	ErrBadCode = errors.New("invalid code")
	// ErrChallengeInvalid is returned when a login-gate challenge is unknown,
	// expired, already consumed, or has exhausted its attempts — it is dead.
	ErrChallengeInvalid = errors.New("challenge invalid or expired")
	// ErrRateLimited is returned when challenge issuance is throttled for the IP.
	ErrRateLimited = errors.New("too many requests")
)

const (
	// maxChallengeAttempts caps wrong-code tries per challenge before it dies.
	maxChallengeAttempts int32 = 5
	// challengeTTL bounds how long a login-gate challenge is valid.
	challengeTTL = 5 * time.Minute
	// challengeIPWindow / challengeIPMax bound how many challenges a single IP can
	// mint in the window (a coarse per-IP throttle on the login gate).
	challengeIPWindow = 10 * time.Minute
	challengeIPMax    = 20
)

// Store is the persistence seam the service depends on (dependency inversion):
// exactly the methods it uses, so unit tests inject an in-memory fake with no DB.
// *PgStore satisfies it.
type Store interface {
	GetUserTOTP(ctx context.Context, userID uuid.UUID) (gen.UserTotp, error)
	UpsertPendingTOTP(ctx context.Context, userID uuid.UUID, ciphertext string) (int64, error)
	GetUserEmail(ctx context.Context, userID uuid.UUID) (string, error)
	ListUnusedRecoveryCodes(ctx context.Context, userID uuid.UUID) ([]gen.ListUnusedRecoveryCodesRow, error)
	CountUnusedRecoveryCodes(ctx context.Context, userID uuid.UUID) (int64, error)
	ConfirmTOTPTx(ctx context.Context, userID uuid.UUID, codeHashes []string) (int64, error)
	DeleteTwoFA(ctx context.Context, userID uuid.UUID) error
	CreateChallenge(ctx context.Context, userID uuid.UUID, hash []byte, ip *netip.Addr, expiresAt time.Time) (uuid.UUID, error)
	GetChallengeByHash(ctx context.Context, hash []byte) (gen.TwoFactorChallenge, error)
	IncrementChallengeAttempts(ctx context.Context, id uuid.UUID) (int32, error)
	ConsumeChallenge(ctx context.Context, id uuid.UUID) (int64, error)
	CountRecentChallengesForIP(ctx context.Context, ip *netip.Addr, since time.Time) (int64, error)
	ConsumeChallengeAndUseRecoveryTx(ctx context.Context, challengeID uuid.UUID, recoveryID *uuid.UUID) (bool, error)
}

// Service implements the twofa business rules. It holds a crypto.ServerKeyring
// (not a per-workspace Keyring) because a TOTP secret is a user-level secret —
// see ServerKeyring's doc for why the per-workspace DEK model does not fit.
type Service struct {
	store   Store
	keyring *crypto.ServerKeyring
	// now is overridable in tests for deterministic TOTP/challenge timing.
	now func() time.Time
}

// NewService builds a Service over store, sealing TOTP secrets with keyring.
func NewService(store Store, keyring *crypto.ServerKeyring) *Service {
	return &Service{store: store, keyring: keyring, now: time.Now}
}

// EnrollResult carries the one-time enrollment payload: the base32 secret and a
// provisioning URI. Recovery codes are NOT issued here — only after confirm.
type EnrollResult struct {
	Secret string // base32, for manual entry
	URI    string // otpauth:// provisioning URI (for a QR code)
}

// Enroll generates a fresh secret, seals it, and stores it UNCONFIRMED. It
// rejects a user who already has a confirmed factor. The raw secret is returned
// exactly once (here); it is never returned again and never logged.
func (s *Service) Enroll(ctx context.Context, userID uuid.UUID) (EnrollResult, error) {
	existing, err := s.store.GetUserTOTP(ctx, userID)
	switch {
	case err == nil:
		if existing.ConfirmedAt.Valid {
			return EnrollResult{}, ErrAlreadyEnrolled
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return EnrollResult{}, err
	}

	secret, err := newTOTPSecret()
	if err != nil {
		return EnrollResult{}, err
	}
	defer zero(secret)

	sealed, err := s.keyring.SealerFor(userID).Seal(secret)
	if err != nil {
		return EnrollResult{}, err
	}
	n, err := s.store.UpsertPendingTOTP(ctx, userID, sealed)
	if err != nil {
		return EnrollResult{}, err
	}
	if n == 0 {
		// The row became confirmed between the check above and here (race).
		return EnrollResult{}, ErrAlreadyEnrolled
	}

	email, err := s.store.GetUserEmail(ctx, userID)
	if err != nil {
		return EnrollResult{}, err
	}
	return EnrollResult{Secret: encodeBase32Secret(secret), URI: provisioningURI(email, secret)}, nil
}

// Confirm verifies a code against the pending secret and, on success, activates
// the factor and mints + stores 10 single-use recovery codes, returning the raw
// codes exactly once. A wrong code returns ErrBadCode and changes nothing.
func (s *Service) Confirm(ctx context.Context, userID uuid.UUID, code string) ([]string, error) {
	totp, err := s.store.GetUserTOTP(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotEnrolled
		}
		return nil, err
	}
	if totp.ConfirmedAt.Valid {
		return nil, ErrAlreadyEnrolled
	}

	secret, err := s.keyring.SealerFor(userID).Open(totp.SecretCiphertext)
	if err != nil {
		return nil, err
	}
	defer zero(secret)
	if !verifyTOTP(secret, code, s.now()) {
		return nil, ErrBadCode
	}

	codes, err := newRecoveryCodes(recoveryCodeCount)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		h, err := hashRecoveryCode(c)
		if err != nil {
			return nil, err
		}
		hashes[i] = h
	}

	n, err := s.store.ConfirmTOTPTx(ctx, userID, hashes)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		// Confirmed concurrently between our read and the tx.
		return nil, ErrAlreadyEnrolled
	}
	return codes, nil
}

// Disable turns off 2FA after proving possession with a fresh TOTP OR a recovery
// code. It deletes every 2FA artifact for the user. Session revocation (a
// security downgrade must not leave other sessions live) is the handler's job,
// via the identity session-revoke seam.
func (s *Service) Disable(ctx context.Context, userID uuid.UUID, code string) error {
	totp, err := s.store.GetUserTOTP(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotEnrolled
		}
		return err
	}
	if !totp.ConfirmedAt.Valid {
		return ErrNotEnrolled
	}
	_, ok, err := s.checkCode(ctx, userID, totp, code)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadCode
	}
	return s.store.DeleteTwoFA(ctx, userID)
}

// StatusResult is the 2FA status surface for the settings UI.
type StatusResult struct {
	Enabled           bool
	RecoveryRemaining int
}

// Status reports whether the user has a confirmed factor and how many recovery
// codes remain.
func (s *Service) Status(ctx context.Context, userID uuid.UUID) (StatusResult, error) {
	totp, err := s.store.GetUserTOTP(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StatusResult{}, nil
		}
		return StatusResult{}, err
	}
	if !totp.ConfirmedAt.Valid {
		return StatusResult{}, nil
	}
	remaining, err := s.store.CountUnusedRecoveryCodes(ctx, userID)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Enabled: true, RecoveryRemaining: int(remaining)}, nil
}

// ChallengeIfRequired implements the login gate's decision. For a user with a
// confirmed factor it mints a single-use, TTL'd challenge and returns
// (rawChallenge, true, nil); for a user WITHOUT a confirmed factor it returns
// ("", false, nil) so login proceeds normally. This is the seam the identity
// login handler consults (satisfying identity's TwoFactorGate). The password has
// already been verified by the caller, so revealing "2FA required" here does not
// leak anything a correct-password holder doesn't already command.
func (s *Service) ChallengeIfRequired(ctx context.Context, userID uuid.UUID, ip string) (string, bool, error) {
	totp, err := s.store.GetUserTOTP(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if !totp.ConfirmedAt.Valid {
		return "", false, nil
	}

	addr := parseIP(ip)
	n, err := s.store.CountRecentChallengesForIP(ctx, addr, s.now().Add(-challengeIPWindow))
	if err != nil {
		return "", false, err
	}
	if n >= challengeIPMax {
		return "", false, ErrRateLimited
	}

	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return "", false, err
	}
	if _, err := s.store.CreateChallenge(ctx, userID, hash, addr, s.now().Add(challengeTTL)); err != nil {
		return "", false, err
	}
	return raw, true, nil
}

// VerifyChallenge completes the login gate: it validates the challenge token and
// the code (TOTP or recovery), and on success consumes the challenge (and marks a
// used recovery code) and returns the user id so the caller issues a session.
//
// Failure modes are fail-closed: an unknown/expired/consumed/exhausted challenge
// is ErrChallengeInvalid (dead); a wrong code is ErrBadCode after decrementing the
// remaining tries (and the challenge dies once tries are exhausted). Whether the
// accepted code was a TOTP or a recovery code is not observable — both succeed
// identically.
func (s *Service) VerifyChallenge(ctx context.Context, rawChallenge, code string) (uuid.UUID, error) {
	ch, err := s.store.GetChallengeByHash(ctx, auth.HashToken(rawChallenge))
	if err != nil {
		return uuid.Nil, ErrChallengeInvalid // unknown challenge (incl. pgx.ErrNoRows)
	}
	if ch.ConsumedAt.Valid || s.now().After(pgxTime(ch.ExpiresAt)) || ch.Attempts >= maxChallengeAttempts {
		return uuid.Nil, ErrChallengeInvalid
	}

	totp, err := s.store.GetUserTOTP(ctx, ch.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The factor was disabled after the challenge was issued: no code can
			// satisfy it. Treat as a wrong code (burns a try) rather than 500.
			return uuid.Nil, s.failAttempt(ctx, ch)
		}
		return uuid.Nil, err
	}
	recoveryID, ok, err := s.checkCode(ctx, ch.UserID, totp, code)
	if err != nil {
		return uuid.Nil, err
	}
	if !ok {
		return uuid.Nil, s.failAttempt(ctx, ch)
	}

	consumed, err := s.store.ConsumeChallengeAndUseRecoveryTx(ctx, ch.ID, recoveryID)
	if err != nil {
		return uuid.Nil, err
	}
	if !consumed {
		return uuid.Nil, ErrChallengeInvalid // lost the consume race
	}
	return ch.UserID, nil
}

// failAttempt records a wrong-code try on a challenge, killing it once the cap is
// reached, and returns ErrBadCode.
func (s *Service) failAttempt(ctx context.Context, ch gen.TwoFactorChallenge) error {
	attempts, err := s.store.IncrementChallengeAttempts(ctx, ch.ID)
	if err != nil {
		return err
	}
	if attempts >= maxChallengeAttempts {
		// Burn the dead challenge so it can never be reused, even before TTL.
		if _, err := s.store.ConsumeChallenge(ctx, ch.ID); err != nil {
			return err
		}
	}
	return ErrBadCode
}

// checkCode reports whether code is a valid TOTP for the user's (confirmed)
// secret or an unused recovery code. On a recovery-code match it returns that
// code's id (so the caller can mark it used); a TOTP match returns a nil id. It
// tries the cheap TOTP path first, falling back to the argon2 recovery scan only
// on a TOTP miss. A user without a confirmed factor yields (nil, false, nil).
func (s *Service) checkCode(ctx context.Context, userID uuid.UUID, totp gen.UserTotp, code string) (*uuid.UUID, bool, error) {
	if !totp.ConfirmedAt.Valid {
		return nil, false, nil
	}
	secret, err := s.keyring.SealerFor(userID).Open(totp.SecretCiphertext)
	if err != nil {
		return nil, false, err
	}
	defer zero(secret)
	if verifyTOTP(secret, code, s.now()) {
		return nil, true, nil
	}

	codes, err := s.store.ListUnusedRecoveryCodes(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	for _, rc := range codes {
		if recoveryCodeMatches(rc.CodeHash, code) {
			id := rc.ID
			return &id, true, nil
		}
	}
	return nil, false, nil
}
