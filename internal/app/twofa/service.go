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
	// It aliases the shared auth sentinel so the identity login handler can map it
	// to HTTP 429 without importing this domain.
	ErrRateLimited = auth.ErrTwoFactorRateLimited
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
	// ConfirmTOTPTx activates the pending factor, seeds recovery codes, and seeds
	// the replay high-water mark with lastStep (the step the confirming code
	// matched) — all in one transaction.
	ConfirmTOTPTx(ctx context.Context, userID uuid.UUID, codeHashes []string, lastStep int64) (int64, error)
	DeleteTwoFA(ctx context.Context, userID uuid.UUID) error
	CreateChallenge(ctx context.Context, userID uuid.UUID, hash []byte, ip *netip.Addr, expiresAt time.Time) (uuid.UUID, error)
	GetChallengeByHash(ctx context.Context, hash []byte) (gen.TwoFactorChallenge, error)
	// ClaimChallengeAttempt atomically increments the challenge's attempt counter
	// iff it is still live (unconsumed) and under maxAttempts, returning the new
	// count. It returns pgx.ErrNoRows when the challenge is already consumed or
	// exhausted — a dead challenge. The single-statement check+increment is the
	// atomic cap that N concurrent wrong guesses cannot exceed.
	ClaimChallengeAttempt(ctx context.Context, id uuid.UUID, maxAttempts int32) (int32, error)
	CountRecentChallengesForIP(ctx context.Context, ip *netip.Addr, since time.Time) (int64, error)
	// ConsumeChallengeAndUseRecoveryTx atomically consumes the challenge, marks a
	// used recovery code (recoveryID != nil), and/or advances the user's TOTP
	// replay high-water mark to totpStep (totpStep > 0 for a TOTP match) — all in
	// one transaction. A lost race on any of the three (already consumed, code
	// already used, step already advanced) returns false.
	ConsumeChallengeAndUseRecoveryTx(ctx context.Context, challengeID uuid.UUID, recoveryID *uuid.UUID, userID uuid.UUID, totpStep int64) (bool, error)
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
	step, ok := verifyTOTP(secret, code, s.now())
	// Reject a code whose step was already consumed (replay). A fresh enrollment's
	// last_step is 0 and any real time-step is > 0, so the first confirm passes;
	// this guards a confirm that reuses a step already burned by another factor op.
	if !ok || step <= totp.LastStep {
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

	n, err := s.store.ConfirmTOTPTx(ctx, userID, hashes, step)
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
	// checkCode rejects a TOTP whose step was already consumed (<= last_step), so a
	// code already used to log in cannot double as the disable proof within its
	// window. The factor row is deleted here, so no step advance is needed.
	_, _, ok, err := s.checkCode(ctx, userID, totp, code)
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
	if ch.ConsumedAt.Valid || s.now().After(pgxTime(ch.ExpiresAt)) {
		return uuid.Nil, ErrChallengeInvalid
	}

	// Atomically claim one attempt slot BEFORE checking the code: the single
	// UPDATE ... WHERE attempts < max both enforces and advances the cap, so N
	// concurrent verifies can never collectively exceed maxChallengeAttempts (the
	// TOCTOU that a read-then-increment allowed). 0 rows means the challenge is
	// already consumed or exhausted — dead.
	if _, err := s.store.ClaimChallengeAttempt(ctx, ch.ID, maxChallengeAttempts); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrChallengeInvalid
		}
		return uuid.Nil, err
	}

	totp, err := s.store.GetUserTOTP(ctx, ch.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The factor was disabled after the challenge was issued: no code can
			// satisfy it. The attempt slot is already burned; report a bad code.
			return uuid.Nil, ErrBadCode
		}
		return uuid.Nil, err
	}
	recoveryID, totpStep, ok, err := s.checkCode(ctx, ch.UserID, totp, code)
	if err != nil {
		return uuid.Nil, err
	}
	if !ok {
		// Wrong (or replayed) code. The attempt slot was already consumed
		// atomically above, so a run of wrong guesses walks the cap down and the
		// challenge dies once ClaimChallengeAttempt stops affecting a row.
		return uuid.Nil, ErrBadCode
	}

	// Consume the challenge and, in the SAME transaction, mark any used recovery
	// code and advance the TOTP replay high-water mark to totpStep. The step
	// advance is guarded (last_step < totpStep), so a code already consumed by a
	// concurrent login loses the race here and this verify is rejected.
	consumed, err := s.store.ConsumeChallengeAndUseRecoveryTx(ctx, ch.ID, recoveryID, ch.UserID, totpStep)
	if err != nil {
		return uuid.Nil, err
	}
	if !consumed {
		return uuid.Nil, ErrChallengeInvalid // lost the consume / step-advance race
	}
	return ch.UserID, nil
}

// checkCode reports whether code is a valid TOTP for the user's (confirmed)
// secret or an unused recovery code. On a recovery-code match it returns that
// code's id (so the caller can mark it used) and a zero step; on a TOTP match it
// returns a nil id and the matched step (> 0, for the caller to advance
// last_step). It tries the cheap TOTP path first, falling back to the argon2
// recovery scan only on a TOTP miss. A TOTP whose matched step was already
// consumed (<= last_step) is a replay and is rejected as NOT ok — it does not fall
// through to the recovery scan. A user without a confirmed factor yields
// (nil, 0, false, nil).
func (s *Service) checkCode(ctx context.Context, userID uuid.UUID, totp gen.UserTotp, code string) (recoveryID *uuid.UUID, totpStep int64, ok bool, err error) {
	if !totp.ConfirmedAt.Valid {
		return nil, 0, false, nil
	}
	secret, err := s.keyring.SealerFor(userID).Open(totp.SecretCiphertext)
	if err != nil {
		return nil, 0, false, err
	}
	defer zero(secret)
	if step, matched := verifyTOTP(secret, code, s.now()); matched {
		if step <= totp.LastStep {
			// Replayed step: reject outright rather than treating it as a candidate
			// recovery code (a matched-but-stale TOTP is never a recovery code).
			return nil, 0, false, nil
		}
		return nil, step, true, nil
	}

	codes, err := s.store.ListUnusedRecoveryCodes(ctx, userID)
	if err != nil {
		return nil, 0, false, err
	}
	for _, rc := range codes {
		if recoveryCodeMatches(rc.CodeHash, code) {
			id := rc.ID
			return &id, 0, true, nil
		}
	}
	return nil, 0, false, nil
}
