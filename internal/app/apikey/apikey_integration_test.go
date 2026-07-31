//go:build integration

package apikey

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

// setup migrates + connects a real Postgres and returns a PgStore, a verifier
// (with an allow-all limiter — the Redis limiter is unit-tested separately), and a
// helper to mint a fresh (workspace, user) pair.
func setup(t *testing.T) (*PgStore, *Service, *Verifier, func() (uuid.UUID, uuid.UUID)) {
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

	store := NewPgStore(pool)
	svc := NewService(store)
	verifier := NewVerifier(store, &fakeLimiter{allow: true}, nil)
	// Synchronous touch in tests: the production async goroutine can outlive the
	// pool Cleanup below and log a "closed pool" WARN. Awaiting it here keeps
	// integration logs clean without changing production's fire-and-forget stamp.
	verifier.touch = func(id uuid.UUID) { _ = store.TouchLastUsed(context.Background(), id) }

	mint := func() (uuid.UUID, uuid.UUID) {
		var ws, uid uuid.UUID
		if err := pool.QueryRow(ctx, "INSERT INTO workspaces (name) VALUES ('apikey-it') RETURNING id").Scan(&ws); err != nil {
			t.Fatalf("insert workspace: %v", err)
		}
		email := "apikey-" + uuid.NewString() + "@it.test"
		if err := pool.QueryRow(ctx, "INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id", email).Scan(&uid); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		return ws, uid
	}
	return store, svc, verifier, mint
}

func bearer(token, remoteAddr string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/contacts", http.NoBody)
	r.Header.Set("Authorization", "Bearer "+token)
	if remoteAddr != "" {
		r.RemoteAddr = remoteAddr
	}
	return r
}

// TestCreateVerifyRoundTrip proves a created key verifies to a principal carrying
// the right workspace and exactly the granted scopes.
func TestCreateVerifyRoundTrip(t *testing.T) {
	_, svc, verifier, mint := setup(t)
	ws, uid := mint()

	_, token, err := svc.Create(context.Background(), CreateInput{
		WorkspaceID: ws, CreatedBy: uid, Name: "ci",
		Scopes: []string{auth.ScopeContactsRead, auth.ScopeCampaignsSend},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	p, ok, err := verifier.Verify(context.Background(), bearer(token, "203.0.113.5:1234"))
	if !ok || err != nil {
		t.Fatalf("Verify: got (ok=%v, err=%v), want success", ok, err)
	}
	if p.Kind != auth.KindAPIKey || p.WorkspaceID != ws.String() {
		t.Fatalf("principal = %+v, want api-key for ws %s", p, ws)
	}
	if !p.HasScope(auth.ScopeContactsRead) || !p.HasScope(auth.ScopeCampaignsSend) || p.HasScope(auth.ScopeMailboxesWrite) {
		t.Fatalf("scopes wrong: %v", p.Scopes)
	}
}

// TestRevokeThenVerifyFails proves a revoked key no longer authenticates.
func TestRevokeThenVerifyFails(t *testing.T) {
	_, svc, verifier, mint := setup(t)
	ws, uid := mint()

	view, token, err := svc.Create(context.Background(), CreateInput{
		WorkspaceID: ws, CreatedBy: uid, Name: "revoke-me", Scopes: []string{auth.ScopeListsRead},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok, err := verifier.Verify(context.Background(), bearer(token, "")); !ok || err != nil {
		t.Fatalf("pre-revoke verify: (ok=%v, err=%v)", ok, err)
	}
	if err := svc.Revoke(context.Background(), ws, view.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok, err := verifier.Verify(context.Background(), bearer(token, "")); ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("post-revoke verify: (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}
	// Revoke is idempotent: a second revoke of the same key still succeeds.
	if err := svc.Revoke(context.Background(), ws, view.ID); err != nil {
		t.Fatalf("idempotent Revoke: %v", err)
	}
}

// TestWorkspaceIsolation proves workspace B cannot see or revoke workspace A's key,
// and A's key keeps working throughout.
func TestWorkspaceIsolation(t *testing.T) {
	_, svc, verifier, mint := setup(t)
	wsA, uidA := mint()
	wsB, _ := mint()

	viewA, tokenA, err := svc.Create(context.Background(), CreateInput{
		WorkspaceID: wsA, CreatedBy: uidA, Name: "a-key", Scopes: []string{auth.ScopeContactsRead},
	})
	if err != nil {
		t.Fatalf("Create A: %v", err)
	}

	// B's list must not include A's key.
	bKeys, err := svc.List(context.Background(), wsB)
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	for _, k := range bKeys {
		if k.ID == viewA.ID {
			t.Fatal("workspace B listed workspace A's key")
		}
	}
	// B cannot revoke A's key (cross-tenant -> 404).
	if err := svc.Revoke(context.Background(), wsB, viewA.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant revoke: err = %v, want ErrNotFound", err)
	}
	// A's key still authenticates.
	if _, ok, err := verifier.Verify(context.Background(), bearer(tokenA, "")); !ok || err != nil {
		t.Fatalf("A key after B's failed revoke: (ok=%v, err=%v)", ok, err)
	}
}

// TestExpiredKeyRejected inserts a key with a past expiry (bypassing the service's
// future-expiry validation, which is the point) and proves it fails closed.
func TestExpiredKeyRejected(t *testing.T) {
	store, _, verifier, mint := setup(t)
	ws, uid := mint()

	prefix, token, hash, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	if _, err := store.Create(context.Background(), CreateParams{
		WorkspaceID: ws, CreatedBy: uid, Name: "expired", Prefix: prefix, SecretHash: hash,
		Scopes: []string{auth.ScopeListsRead}, ExpiresAt: &past,
	}); err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if _, ok, err := verifier.Verify(context.Background(), bearer(token, "")); ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expired verify: (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}
}

// TestIPAllowlistMissRejected proves a request from outside a key's CIDR allowlist
// is denied while one from inside passes — over the real DB round-trip.
func TestIPAllowlistMissRejected(t *testing.T) {
	_, svc, verifier, mint := setup(t)
	ws, uid := mint()

	_, token, err := svc.Create(context.Background(), CreateInput{
		WorkspaceID: ws, CreatedBy: uid, Name: "pinned",
		Scopes: []string{auth.ScopeContactsRead}, IPAllowlist: []string{"203.0.113.0/24"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok, err := verifier.Verify(context.Background(), bearer(token, "198.51.100.7:2222")); ok || !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("allowlist miss: (ok=%v, err=%v), want ErrUnauthorized", ok, err)
	}
	if _, ok, err := verifier.Verify(context.Background(), bearer(token, "203.0.113.7:2222")); !ok || err != nil {
		t.Fatalf("allowlist hit: (ok=%v, err=%v), want success", ok, err)
	}
}
