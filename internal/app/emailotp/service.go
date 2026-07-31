package emailotp

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/notify"
)

// ErrInvalidCode is the SINGLE failure every verify rejection collapses to —
// unknown email, no active code, expired, already consumed, exhausted attempts,
// and wrong code are indistinguishable to the caller, so verify is never an oracle
// distinguishing "wrong code" from "no code" from "no such account".
var ErrInvalidCode = errors.New("invalid or expired code")

const (
	// codeTTL bounds how long an emailed login code is valid.
	codeTTL = 10 * time.Minute
	// maxAttempts caps wrong-code guesses per code before it dies.
	maxAttempts int32 = 5
)

// dummyHash is a real argon2id hash computed once at process start. Verify runs
// codeMatches against it on the unknown-email / no-active-code paths so those take
// the same wall-clock time as a real wrong-code comparison — closing a timing
// side-channel that would otherwise let verify leak whether an email is registered
// or has a code outstanding.
var dummyHash = mustHashDummy()

func mustHashDummy() string {
	h, err := hashCode("000000")
	if err != nil {
		// hashCode only fails if crypto/rand is unreadable, which makes the whole
		// process unusable anyway.
		panic("emailotp: could not compute dummy hash: " + err.Error())
	}
	return h
}

// Service implements the email-OTP business rules: mint + email a login code, and
// verify a presented code. It never issues sessions or consults 2FA itself — the
// handler wires those through identity/twofa seams so an OTP login runs the exact
// same first-factor-completion path as a password login.
type Service struct {
	store  Store
	sender notify.Sender
	// now is overridable in tests for deterministic expiry.
	now func() time.Time
	// dispatch runs the code generation + hash + store + send OFF the request
	// path. Start uses it (after the synchronous user lookup) so a known email
	// costs no more measurable wall-clock time than an unknown one — the same
	// anti-enumeration shape identity.ForgotPassword uses. Defaults to a bare
	// goroutine; tests override it to run inline for determinism.
	dispatch func(func())
	// compare runs the constant-time argon2 comparison. It is a field (defaulting
	// to codeMatches) so EVERY verify outcome — including the ones that reject
	// before reaching a real wrong-code compare — can pay exactly one compare and
	// stay timing-indistinguishable, and a test can assert that.
	compare func(hash, presented string) bool
}

// NewService builds a Service over store, emailing codes via sender.
func NewService(store Store, sender notify.Sender) *Service {
	return &Service{
		store:    store,
		sender:   sender,
		now:      time.Now,
		dispatch: func(f func()) { go f() },
		compare:  codeMatches,
	}
}

// Start requests a passwordless login code for email. It NEVER signals whether the
// address belongs to a real account: an unknown email is a silent no-op and a
// known one mints + emails a code, both returning nil in about the same wall-clock
// time (only the user lookup runs synchronously; everything past it — code
// generation, argon2 hashing, the DB write, and the SMTP send — runs off-path via
// dispatch). Only ONE active code exists per user: a new start invalidates any
// prior unconsumed code. The raw code is never logged.
func (s *Service) Start(ctx context.Context, email string) error {
	userID, err := s.store.GetUserIDByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("emailotp: start lookup failed", "err", err)
		}
		return nil // unknown email (or a lookup failure): no account-existence leak
	}
	s.dispatch(func() {
		// The request context is cancelled once Start returns (before/while this
		// runs), so it must not be reused. A bounded timeout caps a stuck SMTP send.
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		code, err := newNumericCode()
		if err != nil {
			slog.Error("emailotp: code generation failed", "err", err, "user_id", userID)
			return
		}
		hash, err := hashCode(code)
		if err != nil {
			slog.Error("emailotp: code hashing failed", "err", err, "user_id", userID)
			return
		}
		if err := s.store.ReplaceActiveCode(bgCtx, userID, hash, maxAttempts, s.now().Add(codeTTL)); err != nil {
			slog.Error("emailotp: failed to store login code", "err", err, "user_id", userID)
			return
		}
		if err := s.sender.Send(bgCtx, notify.LoginCodeEmail(code)); err != nil {
			slog.Error("emailotp: failed to send login code", "err", err, "user_id", userID)
		}
	})
	return nil
}

// Verify checks a presented code for email and, on success, returns the user id so
// the caller completes the first-factor login (session or 2FA gate). Every failure
// is ErrInvalidCode (see its doc). The attempt slot is claimed ATOMICALLY before
// the constant-time comparison, so N concurrent wrong guesses can never exceed the
// per-code cap, and the code is consumed atomically on success so it is strictly
// single-use.
func (s *Service) Verify(ctx context.Context, email, code string) (uuid.UUID, error) {
	userID, err := s.store.GetUserIDByEmail(ctx, email)
	if err != nil {
		s.compare(dummyHash, code) // equalize timing with the real-account path
		return uuid.Nil, ErrInvalidCode
	}
	row, err := s.store.GetActiveCode(ctx, userID)
	if err != nil {
		s.compare(dummyHash, code) // no active code: still burn the comparison cost
		return uuid.Nil, ErrInvalidCode
	}
	if row.ConsumedAt.Valid || s.now().After(pgxTime(row.ExpiresAt)) {
		// Expired/consumed: pay one compare too, so this outcome is timing-
		// indistinguishable from a real wrong-code compare (returning early here
		// would otherwise leak that this address had a code that just expired).
		s.compare(dummyHash, code)
		return uuid.Nil, ErrInvalidCode
	}
	// Claim one attempt slot BEFORE checking the code: the single UPDATE ... WHERE
	// attempts < max_attempts both enforces and advances the cap, so a run of wrong
	// guesses walks it down and the code dies once no row is affected. 0 rows means
	// the code is already consumed or exhausted — dead.
	if _, err := s.store.ClaimCodeAttempt(ctx, row.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrInvalidCode
		}
		return uuid.Nil, err
	}
	if !s.compare(row.CodeHash, code) {
		// Wrong code: the attempt slot was already consumed atomically above.
		return uuid.Nil, ErrInvalidCode
	}
	n, err := s.store.ConsumeCode(ctx, row.ID)
	if err != nil {
		return uuid.Nil, err
	}
	if n == 0 {
		return uuid.Nil, ErrInvalidCode // lost the consume race with a concurrent verify
	}
	return userID, nil
}
