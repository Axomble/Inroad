package twofa

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// testKeyring builds a real ServerKeyring (pure crypto, no I/O) so unit tests
// exercise genuine seal/open rather than a fake.
func testKeyring(t *testing.T) *crypto.ServerKeyring {
	t.Helper()
	mk := make([]byte, 32)
	for i := range mk {
		mk[i] = byte(i + 7)
	}
	kr, err := crypto.NewServerKeyring(mk)
	if err != nil {
		t.Fatalf("NewServerKeyring: %v", err)
	}
	return kr
}

// fakeStore is an in-memory Store for unit tests — no database.
type fakeStore struct {
	totp       map[uuid.UUID]gen.UserTotp
	recovery   map[uuid.UUID][]gen.UserRecoveryCode
	challenges map[uuid.UUID]*gen.TwoFactorChallenge // by id
	byHash     map[string]uuid.UUID                  // challenge_hash(hex) -> id
	emails     map[uuid.UUID]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		totp:       map[uuid.UUID]gen.UserTotp{},
		recovery:   map[uuid.UUID][]gen.UserRecoveryCode{},
		challenges: map[uuid.UUID]*gen.TwoFactorChallenge{},
		byHash:     map[string]uuid.UUID{},
		emails:     map[uuid.UUID]string{},
	}
}

func (f *fakeStore) GetUserTOTP(_ context.Context, userID uuid.UUID) (gen.UserTotp, error) {
	t, ok := f.totp[userID]
	if !ok {
		return gen.UserTotp{}, pgx.ErrNoRows
	}
	return t, nil
}

func (f *fakeStore) UpsertPendingTOTP(_ context.Context, userID uuid.UUID, ciphertext string) (int64, error) {
	if t, ok := f.totp[userID]; ok && t.ConfirmedAt.Valid {
		return 0, nil // refuse to overwrite a confirmed factor
	}
	f.totp[userID] = gen.UserTotp{UserID: userID, SecretCiphertext: ciphertext}
	return 1, nil
}

func (f *fakeStore) GetUserEmail(_ context.Context, userID uuid.UUID) (string, error) {
	return f.emails[userID], nil
}

func (f *fakeStore) ListUnusedRecoveryCodes(_ context.Context, userID uuid.UUID) ([]gen.ListUnusedRecoveryCodesRow, error) {
	var out []gen.ListUnusedRecoveryCodesRow
	for _, rc := range f.recovery[userID] {
		if !rc.UsedAt.Valid {
			out = append(out, gen.ListUnusedRecoveryCodesRow{ID: rc.ID, CodeHash: rc.CodeHash})
		}
	}
	return out, nil
}

func (f *fakeStore) CountUnusedRecoveryCodes(_ context.Context, userID uuid.UUID) (int64, error) {
	var n int64
	for _, rc := range f.recovery[userID] {
		if !rc.UsedAt.Valid {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) ConfirmTOTPTx(_ context.Context, userID uuid.UUID, hashes []string, lastStep int64) (int64, error) {
	t, ok := f.totp[userID]
	if !ok || t.ConfirmedAt.Valid {
		return 0, nil
	}
	t.ConfirmedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	t.LastStep = lastStep
	f.totp[userID] = t
	f.recovery[userID] = nil
	for _, h := range hashes {
		f.recovery[userID] = append(f.recovery[userID], gen.UserRecoveryCode{ID: uuid.New(), UserID: userID, CodeHash: h})
	}
	return 1, nil
}

func (f *fakeStore) DeleteTwoFA(_ context.Context, userID uuid.UUID) error {
	delete(f.totp, userID)
	delete(f.recovery, userID)
	return nil
}

func (f *fakeStore) CreateChallenge(_ context.Context, userID uuid.UUID, hash []byte, ip *netip.Addr, expiresAt time.Time) (uuid.UUID, error) {
	id := uuid.New()
	f.challenges[id] = &gen.TwoFactorChallenge{
		ID: id, UserID: userID, ChallengeHash: hash, Ip: ip,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}
	f.byHash[string(hash)] = id
	return id, nil
}

func (f *fakeStore) GetChallengeByHash(_ context.Context, hash []byte) (gen.TwoFactorChallenge, error) {
	id, ok := f.byHash[string(hash)]
	if !ok {
		return gen.TwoFactorChallenge{}, pgx.ErrNoRows
	}
	return *f.challenges[id], nil
}

func (f *fakeStore) ClaimChallengeAttempt(_ context.Context, id uuid.UUID, maxAttempts int32) (int32, error) {
	c := f.challenges[id]
	// Mirror the atomic SQL guard: increment only while live and under the cap;
	// otherwise the challenge is dead (pgx.ErrNoRows).
	if c.ConsumedAt.Valid || c.Attempts >= maxAttempts {
		return 0, pgx.ErrNoRows
	}
	c.Attempts++
	return c.Attempts, nil
}

func (f *fakeStore) CountRecentChallengesForIP(_ context.Context, ip *netip.Addr, _ time.Time) (int64, error) {
	// Mirror the SQL IS NOT DISTINCT FROM: a nil ip counts the shared unknown-IP
	// bucket (other nil-ip rows) together, rather than matching nothing.
	var n int64
	for _, c := range f.challenges {
		switch {
		case ip == nil && c.Ip == nil:
			n++
		case ip != nil && c.Ip != nil && *c.Ip == *ip:
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) ConsumeChallengeAndUseRecoveryTx(_ context.Context, challengeID uuid.UUID, recoveryID *uuid.UUID, userID uuid.UUID, totpStep int64) (bool, error) {
	c := f.challenges[challengeID]
	if c.ConsumedAt.Valid {
		return false, nil
	}
	if recoveryID != nil {
		for i := range f.recovery[c.UserID] {
			rc := &f.recovery[c.UserID][i]
			if rc.ID == *recoveryID {
				if rc.UsedAt.Valid {
					return false, nil
				}
				rc.UsedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			}
		}
	}
	if totpStep > 0 {
		// Mirror the AdvanceTOTPStep last_step < totpStep guard: a stale/equal step
		// loses the race and the whole verify is rejected without consuming.
		t := f.totp[userID]
		if t.LastStep >= totpStep {
			return false, nil
		}
		t.LastStep = totpStep
		f.totp[userID] = t
	}
	c.ConsumedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return true, nil
}

// enrollAndConfirm drives a full enroll→confirm for a user, returning the raw
// TOTP secret bytes and the one-time recovery codes.
func enrollAndConfirm(t *testing.T, svc *Service, store *fakeStore, uid uuid.UUID) ([]byte, []string) {
	t.Helper()
	ctx := context.Background()
	store.emails[uid] = "user@example.test"
	res, err := svc.Enroll(ctx, uid)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	secret, err := base32NoPad.DecodeString(res.Secret)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	code := totpAt(secret, time.Now())
	codes, err := svc.Confirm(ctx, uid, code)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("got %d recovery codes, want %d", len(codes), recoveryCodeCount)
	}
	return secret, codes
}

func TestEnrollConfirmFlow(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()

	secret, _ := enrollAndConfirm(t, svc, store, uid)

	// The stored secret is sealed, not plaintext.
	if store.totp[uid].SecretCiphertext == string(secret) {
		t.Fatal("secret stored in plaintext")
	}
	st, err := svc.Status(context.Background(), uid)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Enabled || st.RecoveryRemaining != recoveryCodeCount {
		t.Fatalf("status = %+v, want enabled with %d codes", st, recoveryCodeCount)
	}
}

func TestEnrollRejectsWhenAlreadyEnrolled(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	enrollAndConfirm(t, svc, store, uid)

	if _, err := svc.Enroll(context.Background(), uid); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("Enroll after confirm: got %v, want ErrAlreadyEnrolled", err)
	}
}

func TestConfirmWrongCode(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	store.emails[uid] = "u@e.test"
	if _, err := svc.Enroll(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Confirm(context.Background(), uid, "000000"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("Confirm wrong code: got %v, want ErrBadCode", err)
	}
	// Still not confirmed.
	if store.totp[uid].ConfirmedAt.Valid {
		t.Fatal("a wrong confirm code must not activate the factor")
	}
}

func TestGateNotRequiredWithoutConfirmedFactor(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()

	// No factor at all.
	_, required, err := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if err != nil || required {
		t.Fatalf("no factor: required=%v err=%v, want false/nil", required, err)
	}

	// Pending (unconfirmed) factor must NOT gate login.
	store.emails[uid] = "u@e.test"
	if _, err := svc.Enroll(context.Background(), uid); err != nil {
		t.Fatal(err)
	}
	_, required, err = svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if err != nil || required {
		t.Fatalf("pending factor: required=%v err=%v, want false/nil", required, err)
	}
}

// TestNullIPThrottleSharesBucket proves the per-IP throttle fails CLOSED on an
// empty/unparseable client IP: unknown-IP callers share one bucket (parseIP -> nil
// stored as NULL, counted together via IS NOT DISTINCT FROM), so the throttle still
// fires rather than silently skipping.
func TestNullIPThrottleSharesBucket(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	enrollAndConfirm(t, svc, store, uid)

	// Mint challengeIPMax challenges from an empty (unknown) IP.
	for i := 0; i < challengeIPMax; i++ {
		if _, required, err := svc.ChallengeIfRequired(context.Background(), uid, ""); err != nil || !required {
			t.Fatalf("mint %d from unknown IP: required=%v err=%v", i, required, err)
		}
	}
	// The next unknown-IP challenge is throttled — the bucket counted the nulls
	// together instead of failing open.
	if _, _, err := svc.ChallengeIfRequired(context.Background(), uid, ""); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("over-cap unknown IP: got %v, want ErrRateLimited", err)
	}
}

func TestGateRequiredMintsChallenge(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	enrollAndConfirm(t, svc, store, uid)

	challenge, required, err := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if err != nil || !required {
		t.Fatalf("confirmed factor: required=%v err=%v, want true/nil", required, err)
	}
	if challenge == "" {
		t.Fatal("expected a non-empty challenge token")
	}
	// The raw challenge is not what's stored (only its hash is).
	if _, ok := store.byHash[string(auth.HashToken(challenge))]; !ok {
		t.Fatal("challenge stored under a hash that doesn't match the raw token")
	}
}

func TestVerifyChallengeWithTOTP(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	secret, _ := enrollAndConfirm(t, svc, store, uid)
	// Confirm consumed the current step (advanced last_step); a real login is a
	// later step, so drive the service clock forward one period and use that step.
	loginTime := time.Now().Add(totpPeriodSec * time.Second)
	svc.now = func() time.Time { return loginTime }
	challenge, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")

	gotUID, err := svc.VerifyChallenge(context.Background(), challenge, totpAt(secret, loginTime))
	if err != nil {
		t.Fatalf("VerifyChallenge: %v", err)
	}
	if gotUID != uid {
		t.Fatalf("verified uid = %v, want %v", gotUID, uid)
	}
	// Single-use: the same challenge can't be replayed.
	if _, err := svc.VerifyChallenge(context.Background(), challenge, totpAt(secret, loginTime)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("challenge replay: got %v, want ErrChallengeInvalid", err)
	}
}

// TestVerifyChallengeRejectsTOTPStepReplay proves the core FIX 1 property: a valid
// TOTP code accepted for one challenge cannot satisfy a SECOND challenge within its
// validity window (its step is now <= last_step), while a NEXT-step code still
// works. Recovery codes are exercised separately; here every code is a TOTP.
func TestVerifyChallengeRejectsTOTPStepReplay(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	secret, _ := enrollAndConfirm(t, svc, store, uid)

	// First login at step N+1 (after confirm's step): succeeds.
	stepN1 := time.Now().Add(totpPeriodSec * time.Second)
	svc.now = func() time.Time { return stepN1 }
	code := totpAt(secret, stepN1)
	ch1, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if _, err := svc.VerifyChallenge(context.Background(), ch1, code); err != nil {
		t.Fatalf("first verify: %v", err)
	}

	// Second challenge, SAME still-valid code (same step): must be rejected as a
	// consumed-step replay, not accepted.
	ch2, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if _, err := svc.VerifyChallenge(context.Background(), ch2, code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("step replay: got %v, want ErrBadCode", err)
	}

	// A NEXT-step code (step N+2) on a fresh challenge still works.
	stepN2 := stepN1.Add(totpPeriodSec * time.Second)
	svc.now = func() time.Time { return stepN2 }
	ch3, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if _, err := svc.VerifyChallenge(context.Background(), ch3, totpAt(secret, stepN2)); err != nil {
		t.Fatalf("next-step verify: %v", err)
	}
}

// TestCrossUserTOTPIsolation proves TOTP is user-pinned: user A's valid code never
// satisfies user B's challenge (each verify opens B's own sealed secret under B's
// keyring), and B's enrollment URI carries B's own email — no cross-user bleed.
func TestCrossUserTOTPIsolation(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	a, b := uuid.New(), uuid.New()

	secretA, _ := enrollAndConfirm(t, svc, store, a)
	secretB, _ := enrollAndConfirm(t, svc, store, b)

	// The Enroll provisioning URI is pinned to the enrolling user's OWN email — the
	// URI is built from s.store.GetUserEmail(userID), not any caller input.
	c := uuid.New()
	store.emails[c] = "carol@example.test"
	enrolC, err := svc.Enroll(context.Background(), c)
	if err != nil {
		t.Fatalf("Enroll C: %v", err)
	}
	if !strings.Contains(enrolC.URI, "carol@example.test") {
		t.Fatalf("C's provisioning URI not pinned to C's email: %s", enrolC.URI)
	}

	// A code minted from A's secret must NOT verify B's challenge (different step
	// key entirely), even at a later step. Use a next-step clock so neither code is
	// a confirm-step replay.
	loginTime := time.Now().Add(totpPeriodSec * time.Second)
	svc.now = func() time.Time { return loginTime }
	chB, _, _ := svc.ChallengeIfRequired(context.Background(), b, "203.0.113.5")
	if _, err := svc.VerifyChallenge(context.Background(), chB, totpAt(secretA, loginTime)); !errors.Is(err, ErrBadCode) {
		t.Fatalf("A's code against B's challenge: got %v, want ErrBadCode", err)
	}
	// B's own code satisfies B's challenge.
	if _, err := svc.VerifyChallenge(context.Background(), chB, totpAt(secretB, loginTime)); err != nil {
		t.Fatalf("B's own code against B's challenge: %v", err)
	}
}

// TestRecoveryCodeExhaustion consumes all recovery codes and proves the count
// reaches zero and a spent code no longer verifies.
func TestRecoveryCodeExhaustion(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	_, codes := enrollAndConfirm(t, svc, store, uid)

	for i, c := range codes {
		ch, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
		if _, err := svc.VerifyChallenge(context.Background(), ch, c); err != nil {
			t.Fatalf("verify recovery code %d: %v", i, err)
		}
	}
	st, err := svc.Status(context.Background(), uid)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.RecoveryRemaining != 0 {
		t.Fatalf("recovery remaining = %d, want 0 after exhausting all %d", st.RecoveryRemaining, recoveryCodeCount)
	}
	// A spent code no longer verifies.
	ch, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if _, err := svc.VerifyChallenge(context.Background(), ch, codes[0]); !errors.Is(err, ErrBadCode) {
		t.Fatalf("spent recovery code: got %v, want ErrBadCode", err)
	}
}

func TestVerifyChallengeWithRecoveryCodeSingleUse(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	_, codes := enrollAndConfirm(t, svc, store, uid)

	ch1, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if _, err := svc.VerifyChallenge(context.Background(), ch1, codes[0]); err != nil {
		t.Fatalf("verify with recovery code: %v", err)
	}
	// The used recovery code is now dead — a NEW challenge must reject it.
	ch2, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if _, err := svc.VerifyChallenge(context.Background(), ch2, codes[0]); !errors.Is(err, ErrBadCode) {
		t.Fatalf("reused recovery code: got %v, want ErrBadCode", err)
	}
	// A different, still-unused code works.
	ch3, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	if _, err := svc.VerifyChallenge(context.Background(), ch3, codes[1]); err != nil {
		t.Fatalf("second recovery code: %v", err)
	}
	remaining, _ := store.CountUnusedRecoveryCodes(context.Background(), uid)
	if remaining != int64(recoveryCodeCount-2) {
		t.Fatalf("remaining codes = %d, want %d", remaining, recoveryCodeCount-2)
	}
}

func TestVerifyChallengeTryCap(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	enrollAndConfirm(t, svc, store, uid)
	challenge, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")

	for i := int32(0); i < maxChallengeAttempts; i++ {
		if _, err := svc.VerifyChallenge(context.Background(), challenge, "000000"); !errors.Is(err, ErrBadCode) {
			t.Fatalf("attempt %d: got %v, want ErrBadCode", i, err)
		}
	}
	// Exhausted: the challenge is now dead — even a further attempt is rejected as
	// an invalid (not merely wrong-code) challenge.
	if _, err := svc.VerifyChallenge(context.Background(), challenge, "000000"); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("post-exhaustion: got %v, want ErrChallengeInvalid", err)
	}
}

func TestVerifyChallengeExpired(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	secret, _ := enrollAndConfirm(t, svc, store, uid)

	// Mint a challenge, then move the service clock past its TTL.
	challenge, _, _ := svc.ChallengeIfRequired(context.Background(), uid, "203.0.113.5")
	svc.now = func() time.Time { return time.Now().Add(challengeTTL + time.Minute) }

	if _, err := svc.VerifyChallenge(context.Background(), challenge, totpAt(secret, time.Now())); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("expired challenge: got %v, want ErrChallengeInvalid", err)
	}
}

func TestVerifyChallengeUnknownToken(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	if _, err := svc.VerifyChallenge(context.Background(), "not-a-real-challenge", "000000"); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("unknown challenge: got %v, want ErrChallengeInvalid", err)
	}
}

func TestDisableRequiresValidCode(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	secret, _ := enrollAndConfirm(t, svc, store, uid)

	if err := svc.Disable(context.Background(), uid, "000000"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("disable with wrong code: got %v, want ErrBadCode", err)
	}
	if _, ok := store.totp[uid]; !ok {
		t.Fatal("a failed disable must not remove the factor")
	}
	// Confirm consumed the current step; disable proof must be a LATER step's code.
	disableTime := time.Now().Add(totpPeriodSec * time.Second)
	svc.now = func() time.Time { return disableTime }
	if err := svc.Disable(context.Background(), uid, totpAt(secret, disableTime)); err != nil {
		t.Fatalf("disable with valid code: %v", err)
	}
	if _, ok := store.totp[uid]; ok {
		t.Fatal("disable must remove the factor")
	}
}

func TestDisableWithRecoveryCode(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	uid := uuid.New()
	_, codes := enrollAndConfirm(t, svc, store, uid)

	if err := svc.Disable(context.Background(), uid, codes[3]); err != nil {
		t.Fatalf("disable with recovery code: %v", err)
	}
	if _, ok := store.totp[uid]; ok {
		t.Fatal("disable must remove the factor")
	}
}

func TestDisableNotEnrolled(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, testKeyring(t))
	if err := svc.Disable(context.Background(), uuid.New(), "000000"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("disable un-enrolled: got %v, want ErrNotEnrolled", err)
	}
}
