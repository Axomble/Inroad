//go:build integration

package passkey

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These tests drive the PgStore against a real Postgres to prove the SQL-level
// invariants the passkey service leans on: signature-counter forward-only advance
// (clone/replay defense), single-use + TTL'd challenges, own-only deletion, and the
// unique-credential constraint. The full browser WebAuthn ceremony (register →
// login → session) requires a software authenticator and is integration-deferred;
// the clone DECISION is proven deterministically in TestCloneDetectionSemantics.

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

func setup(t *testing.T) (*pgxpool.Pool, *PgStore, *gen.Queries) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// Clean slate for the credential/challenge tables (users cascade-delete them).
	if _, err := pool.Exec(ctx, "TRUNCATE webauthn_challenges, webauthn_credentials"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool, NewPgStore(pool), gen.New(pool)
}

func mkUser(t *testing.T, q *gen.Queries) uuid.UUID {
	t.Helper()
	u, err := q.CreateUser(context.Background(), gen.CreateUserParams{
		Email:        "pk-" + uuid.NewString() + "@example.test",
		PasswordHash: "x",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}

func mkCred(t *testing.T, s *PgStore, user uuid.UUID, credID []byte, signCount int64) gen.WebauthnCredential {
	t.Helper()
	row, err := s.CreateCredential(context.Background(), gen.CreateWebAuthnCredentialParams{
		UserID: user, CredentialID: credID, PublicKey: []byte{1, 2, 3},
		SignCount: signCount, Transports: "internal", AttestationType: "none", Label: "test",
	})
	if err != nil {
		t.Fatalf("create credential: %v", err)
	}
	return row
}

func TestSignCountForwardOnly(t *testing.T) {
	_, store, q := setup(t)
	ctx := context.Background()
	user := mkUser(t, q)
	other := mkUser(t, q)
	cred := mkCred(t, store, user, []byte("cred-fwd"), 5)

	// Regression is rejected: 0 rows, so a cloned authenticator presenting a stale
	// counter cannot advance (belt-and-braces over the library clone warning).
	if n, err := store.TouchSignCount(ctx, cred.ID, user, 3); err != nil || n != 0 {
		t.Fatalf("regression: n=%d err=%v, want n=0", n, err)
	}
	// Equal is allowed (zero-counter authenticators legitimately never increment).
	if n, err := store.TouchSignCount(ctx, cred.ID, user, 5); err != nil || n != 1 {
		t.Fatalf("equal: n=%d err=%v, want n=1", n, err)
	}
	// Forward advance is applied.
	if n, err := store.TouchSignCount(ctx, cred.ID, user, 9); err != nil || n != 1 {
		t.Fatalf("forward: n=%d err=%v, want n=1", n, err)
	}
	// A different user cannot advance this credential (user-pinned).
	if n, err := store.TouchSignCount(ctx, cred.ID, other, 20); err != nil || n != 0 {
		t.Fatalf("cross-user: n=%d err=%v, want n=0", n, err)
	}
	// The persisted counter is the last accepted forward value.
	got, err := store.GetCredentialByCredentialID(ctx, []byte("cred-fwd"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SignCount != 9 {
		t.Fatalf("stored sign count = %d, want 9", got.SignCount)
	}
}

func TestChallengeSingleUseAndTTL(t *testing.T) {
	_, store, q := setup(t)
	ctx := context.Background()
	user := mkUser(t, q)

	// A live register challenge is consumed exactly once.
	rawLive, hashLive, _ := auth.NewOpaqueToken()
	if _, err := store.CreateChallenge(ctx, hashLive, &user, []byte(`{"c":"x"}`), kindRegister, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("create live challenge: %v", err)
	}
	ch, err := store.ConsumeChallenge(ctx, auth.HashToken(rawLive))
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if ch.Kind != kindRegister || uuid.UUID(ch.UserID.Bytes) != user {
		t.Fatalf("consumed challenge wrong: kind=%q user=%v", ch.Kind, ch.UserID)
	}
	if _, err := store.ConsumeChallenge(ctx, auth.HashToken(rawLive)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("second consume: got %v, want pgx.ErrNoRows (single-use)", err)
	}

	// An expired challenge is never consumable (TTL enforced in SQL).
	rawDead, hashDead, _ := auth.NewOpaqueToken()
	if _, err := store.CreateChallenge(ctx, hashDead, nil, []byte(`{"c":"y"}`), kindLogin, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("create expired challenge: %v", err)
	}
	if _, err := store.ConsumeChallenge(ctx, auth.HashToken(rawDead)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired consume: got %v, want pgx.ErrNoRows (TTL)", err)
	}
}

func TestCredentialOwnershipIsolation(t *testing.T) {
	_, store, q := setup(t)
	ctx := context.Background()
	userA := mkUser(t, q)
	userB := mkUser(t, q)
	cred := mkCred(t, store, userA, []byte("cred-iso"), 0)

	// userB cannot delete userA's credential (own-only → 0 rows).
	if n, err := store.DeleteCredential(ctx, cred.ID, userB); err != nil || n != 0 {
		t.Fatalf("foreign delete: n=%d err=%v, want n=0", n, err)
	}
	// A discoverable login resolves the credential to its true owner, userA.
	got, err := store.GetCredentialByCredentialID(ctx, []byte("cred-iso"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.UserID != userA {
		t.Fatalf("resolved owner = %v, want userA %v", got.UserID, userA)
	}
	// userA deletes their own credential (1 row).
	if n, err := store.DeleteCredential(ctx, cred.ID, userA); err != nil || n != 1 {
		t.Fatalf("own delete: n=%d err=%v, want n=1", n, err)
	}
}

func TestDuplicateCredentialRejected(t *testing.T) {
	_, store, q := setup(t)
	ctx := context.Background()
	user := mkUser(t, q)
	mkCred(t, store, user, []byte("cred-dup"), 0)

	_, err := store.CreateCredential(ctx, gen.CreateWebAuthnCredentialParams{
		UserID: user, CredentialID: []byte("cred-dup"), PublicKey: []byte{9},
		Transports: "", AttestationType: "none", Label: "dup",
	})
	if !isUniqueViolation(err) {
		t.Fatalf("duplicate credential id: got %v, want unique violation", err)
	}
}
