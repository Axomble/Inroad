package passkey

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore is an in-memory Store for unit tests — no database. It records
// challenges by session-key hash and enforces single-use consumption, so the
// service's challenge lifecycle is exercised without Postgres.
type fakeStore struct {
	creds      map[uuid.UUID]gen.WebauthnCredential // by row id
	byCredID   map[string]uuid.UUID                 // hex(credential_id) -> row id
	challenges map[string]gen.WebauthnChallenge     // hex(session_key) -> challenge
	consumed   map[string]bool
	emails     map[uuid.UUID]string

	createErr    error
	deleteRows   int64
	touchedCount int64
	touchedID    uuid.UUID
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		creds:      map[uuid.UUID]gen.WebauthnCredential{},
		byCredID:   map[string]uuid.UUID{},
		challenges: map[string]gen.WebauthnChallenge{},
		consumed:   map[string]bool{},
		emails:     map[uuid.UUID]string{},
		deleteRows: 1,
	}
}

func (f *fakeStore) CreateCredential(_ context.Context, arg gen.CreateWebAuthnCredentialParams) (gen.WebauthnCredential, error) {
	if f.createErr != nil {
		return gen.WebauthnCredential{}, f.createErr
	}
	id := uuid.New()
	row := gen.WebauthnCredential{
		ID: id, UserID: arg.UserID, CredentialID: arg.CredentialID, PublicKey: arg.PublicKey,
		SignCount: arg.SignCount, Aaguid: arg.Aaguid, Transports: arg.Transports,
		AttestationType: arg.AttestationType, BackupEligible: arg.BackupEligible,
		BackupState: arg.BackupState, Label: arg.Label,
	}
	f.creds[id] = row
	f.byCredID[hex.EncodeToString(arg.CredentialID)] = id
	return row, nil
}

func (f *fakeStore) GetCredentialByCredentialID(_ context.Context, credentialID []byte) (gen.WebauthnCredential, error) {
	id, ok := f.byCredID[hex.EncodeToString(credentialID)]
	if !ok {
		return gen.WebauthnCredential{}, pgx.ErrNoRows
	}
	return f.creds[id], nil
}

func (f *fakeStore) ListCredentialsByUser(_ context.Context, userID uuid.UUID) ([]gen.WebauthnCredential, error) {
	var out []gen.WebauthnCredential
	for _, c := range f.creds {
		if c.UserID == userID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) TouchSignCount(_ context.Context, id, _ uuid.UUID, signCount int64) (int64, error) {
	f.touchedID = id
	f.touchedCount = signCount
	return 1, nil
}

func (f *fakeStore) DeleteCredential(_ context.Context, _, _ uuid.UUID) (int64, error) {
	return f.deleteRows, nil
}

func (f *fakeStore) CreateChallenge(_ context.Context, sessionKey []byte, userID *uuid.UUID, sessionData []byte, kind string, expiresAt time.Time) (uuid.UUID, error) {
	ch := gen.WebauthnChallenge{
		SessionKey: sessionKey, SessionData: sessionData, Kind: kind,
		ExpiresAt: pgxTimestamp(expiresAt),
	}
	if userID != nil {
		ch.UserID = pgtype.UUID{Bytes: *userID, Valid: true}
	}
	f.challenges[hex.EncodeToString(sessionKey)] = ch
	return uuid.New(), nil
}

func (f *fakeStore) ConsumeChallenge(_ context.Context, sessionKey []byte) (gen.WebauthnChallenge, error) {
	k := hex.EncodeToString(sessionKey)
	ch, ok := f.challenges[k]
	if !ok || f.consumed[k] {
		return gen.WebauthnChallenge{}, pgx.ErrNoRows // unknown or already consumed
	}
	f.consumed[k] = true // single-use
	return ch, nil
}

func (f *fakeStore) GetUserEmail(_ context.Context, userID uuid.UUID) (string, error) {
	if e, ok := f.emails[userID]; ok {
		return e, nil
	}
	return "user@example.test", nil
}

// testWeb builds a real library instance bound to a test RP so the Begin* paths
// exercise genuine option/challenge generation (no authenticator needed).
func testWeb(t *testing.T) *webauthn.WebAuthn {
	t.Helper()
	w, err := NewWebAuthn("localhost", "http://localhost:8080")
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	return w
}

func TestNotConfiguredWhenNilWeb(t *testing.T) {
	svc := NewService(nil, newFakeStore())
	ctx := context.Background()

	if _, err := svc.BeginRegistration(ctx, uuid.New()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("BeginRegistration: got %v, want ErrNotConfigured", err)
	}
	if _, err := svc.BeginLogin(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("BeginLogin: got %v, want ErrNotConfigured", err)
	}
	if err := svc.FinishRegistration(ctx, uuid.New(), "s", []byte("{}"), ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("FinishRegistration: got %v, want ErrNotConfigured", err)
	}
	if _, err := svc.FinishLogin(ctx, "s", []byte("{}")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("FinishLogin: got %v, want ErrNotConfigured", err)
	}
}

func TestChallengeSingleUse(t *testing.T) {
	store := newFakeStore()
	svc := NewService(testWeb(t), store)
	ctx := context.Background()

	opts, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if opts.SessionID == "" || opts.PublicKey == nil {
		t.Fatalf("BeginLogin returned empty options: %+v", opts)
	}
	// The challenge is persisted server-side with a nil user (discoverable) and kind
	// login — never trusting a client-echoed challenge.
	ch, ok := store.challenges[hex.EncodeToString(auth.HashToken(opts.SessionID))]
	if !ok {
		t.Fatal("login challenge was not persisted server-side")
	}
	if ch.Kind != kindLogin || ch.UserID.Valid {
		t.Fatalf("login challenge wrong shape: kind=%q userValid=%v", ch.Kind, ch.UserID.Valid)
	}

	// First finish consumes the challenge (parse fails on the empty assertion, so it
	// is a flat ErrAssertion — but the challenge is now burned).
	if _, err := svc.FinishLogin(ctx, opts.SessionID, []byte("{}")); !errors.Is(err, ErrAssertion) {
		t.Fatalf("first FinishLogin: got %v, want ErrAssertion", err)
	}
	// Second finish with the SAME session id is rejected as a dead challenge.
	if _, err := svc.FinishLogin(ctx, opts.SessionID, []byte("{}")); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("second FinishLogin: got %v, want ErrChallengeInvalid (single-use)", err)
	}
}

func TestFinishLoginDeadChallenge(t *testing.T) {
	svc := NewService(testWeb(t), newFakeStore())
	// Never begun → unknown session id → dead challenge.
	if _, err := svc.FinishLogin(context.Background(), "never-issued", []byte("{}")); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("got %v, want ErrChallengeInvalid", err)
	}
}

func TestFinishRegistrationKindMismatch(t *testing.T) {
	store := newFakeStore()
	svc := NewService(testWeb(t), store)
	ctx := context.Background()

	// A LOGIN challenge presented to the registration finish must be rejected: the
	// ceremony kind is bound server-side.
	opts, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	if err := svc.FinishRegistration(ctx, uuid.New(), opts.SessionID, []byte("{}"), ""); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("got %v, want ErrChallengeInvalid (kind mismatch)", err)
	}
}

func TestFinishRegistrationUserMismatch(t *testing.T) {
	store := newFakeStore()
	svc := NewService(testWeb(t), store)
	ctx := context.Background()

	userA := uuid.New()
	opts, err := svc.BeginRegistration(ctx, userA)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	// A different user presenting userA's register challenge is rejected — the
	// challenge is user-pinned even beyond the authed context.
	userB := uuid.New()
	if err := svc.FinishRegistration(ctx, userB, opts.SessionID, []byte("{}"), ""); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("got %v, want ErrChallengeInvalid (user mismatch)", err)
	}
}

func TestDeleteOwnOnly(t *testing.T) {
	store := newFakeStore()
	svc := NewService(testWeb(t), store)
	ctx := context.Background()

	// A foreign/missing id deletes 0 rows → ErrNotFound (no cross-user probe).
	store.deleteRows = 0
	if err := svc.Delete(ctx, uuid.New(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete foreign: got %v, want ErrNotFound", err)
	}
	// The caller's own credential deletes 1 row → nil.
	store.deleteRows = 1
	if err := svc.Delete(ctx, uuid.New(), uuid.New()); err != nil {
		t.Fatalf("delete own: got %v, want nil", err)
	}
}

func TestListProjectionOmitsKeyMaterial(t *testing.T) {
	store := newFakeStore()
	svc := NewService(testWeb(t), store)
	ctx := context.Background()

	user := uuid.New()
	used := time.Now().UTC().Truncate(time.Second)
	id := uuid.New()
	store.creds[id] = gen.WebauthnCredential{
		ID: id, UserID: user, CredentialID: []byte{1, 2, 3}, PublicKey: []byte{9, 9, 9},
		SignCount: 4, Transports: "internal,hybrid", Label: "Phone",
		CreatedAt:  pgtype.Timestamptz{Time: used, Valid: true},
		LastUsedAt: pgtype.Timestamptz{Time: used, Valid: true},
	}

	got, err := svc.List(ctx, user)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List len = %d, want 1", len(got))
	}
	info := got[0]
	if info.ID != id.String() || info.Label != "Phone" {
		t.Fatalf("List projection wrong: %+v", info)
	}
	if len(info.Transports) != 2 || info.Transports[0] != "internal" || info.Transports[1] != "hybrid" {
		t.Fatalf("transports not projected: %+v", info.Transports)
	}
	if info.LastUsedAt == nil || !info.LastUsedAt.Equal(used) {
		t.Fatalf("last_used_at not projected: %+v", info.LastUsedAt)
	}
	// CredentialInfo has no public-key/sign-count field by construction; this is the
	// compile-time guarantee that the DTO never leaks sensitive credential state.
}

// TestCloneDetectionSemantics proves the exact library behavior FinishLogin relies
// on for clone detection: a signature counter that does not strictly increase (for a
// counter-supporting authenticator) raises CloneWarning, which the service maps to
// ErrCloneDetected and refuses to persist. Driving a full assertion requires a
// browser authenticator (integration-deferred), so this crafts the counter decision
// directly.
func TestCloneDetectionSemantics(t *testing.T) {
	cases := []struct {
		name     string
		stored   uint32
		incoming uint32
		wantWarn bool
	}{
		{"regression is a clone", 10, 5, true},
		{"equal nonzero is a clone", 10, 10, true},
		{"forward move is fine", 10, 11, false},
		{"zero-counter authenticator is fine", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := webauthn.Authenticator{SignCount: tc.stored}
			a.UpdateCounter(tc.incoming)
			if a.CloneWarning != tc.wantWarn {
				t.Fatalf("CloneWarning = %v, want %v (stored=%d incoming=%d)",
					a.CloneWarning, tc.wantWarn, tc.stored, tc.incoming)
			}
		})
	}
}
