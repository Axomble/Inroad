//go:build integration

package idempotency

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// These tests exercise PgStore against real Postgres: the atomic expiry-reclaim
// in InsertIdempotencyKey's ON CONFLICT ... DO UPDATE ... WHERE, GetIdempotencyKey's
// own age filter, DeleteIdempotencyKey, and workspace pinning. Before this file,
// only a hand-written Go fake (internal/platform/httpx/idempotency_test.go)
// exercised this store's intended semantics -- this closes the live-DB gap on the
// actual SQL. Docker must be up.

// setup connects a fresh pool against the migrated test database and returns
// the store under test.
func setup(t *testing.T) (*pgxpool.Pool, *PgStore) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, NewPgStore(pool)
}

// backdate ages (workspaceID, key)'s created_at by 25 hours -- past the 24h
// retention window -- via a direct UPDATE (rather than seeding it stale to begin
// with), so InsertIdempotencyKey's atomic reclaim and GetIdempotencyKey's age
// filter are exercised on a row that genuinely aged out, without waiting real
// time or running the maintenance sweep first.
func backdate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID uuid.UUID, key string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE idempotency_keys SET created_at = now() - interval '25 hours' WHERE workspace_id = $1 AND key = $2`,
		workspaceID, key); err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

// A fresh key claims cleanly: the row exists, with no response recorded yet.
func TestTryInsertClaimsAFreshKey(t *testing.T) {
	ctx := context.Background()
	_, store := setup(t)
	ws := uuid.New().String()

	inserted, err := store.TryInsert(ctx, ws, "k1", []byte("hash-1"))
	if err != nil {
		t.Fatalf("TryInsert: %v", err)
	}
	if !inserted {
		t.Fatal("fresh key was not claimed")
	}

	rec, found, err := store.Get(ctx, ws, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("fresh row not found immediately after insert")
	}
	if rec.StatusCode != nil {
		t.Errorf("StatusCode = %d, want nil (no response recorded yet)", *rec.StatusCode)
	}
	if string(rec.RequestHash) != "hash-1" {
		t.Errorf("RequestHash = %q, want hash-1", rec.RequestHash)
	}
}

// A same-key, still-unexpired, unresolved conflict must not be reclaimed: this is
// the "in flight" case the middleware relies on TryInsert=false to detect.
func TestTryInsertConflictsWhileUnexpiredAndUnresolved(t *testing.T) {
	ctx := context.Background()
	_, store := setup(t)
	ws := uuid.New().String()

	if inserted, err := store.TryInsert(ctx, ws, "k1", []byte("hash-1")); err != nil || !inserted {
		t.Fatalf("first TryInsert: inserted=%v err=%v", inserted, err)
	}

	// Same key while it is still inside its window and no response was ever
	// recorded: the ON CONFLICT DO UPDATE's WHERE guard does not fire, so
	// RETURNING yields no row and TryInsert maps that to inserted=false, not an
	// error.
	inserted, err := store.TryInsert(ctx, ws, "k1", []byte("hash-1"))
	if err != nil {
		t.Fatalf("second TryInsert: %v", err)
	}
	if inserted {
		t.Fatal("an in-flight, unexpired key was reclaimed")
	}
}

// An EXPIRED conflicting row is reclaimed atomically -- by ANY new request, not
// just a retry of the same one (rule 6): the reclaim overwrites the hash and
// resets the response even though the new request's hash differs from the old.
func TestTryInsertReclaimsAnExpiredRowWithADifferentHash(t *testing.T) {
	ctx := context.Background()
	pool, store := setup(t)
	ws := uuid.New().String()

	if inserted, err := store.TryInsert(ctx, ws, "k1", []byte("hash-1")); err != nil || !inserted {
		t.Fatalf("first TryInsert: inserted=%v err=%v", inserted, err)
	}
	backdate(t, ctx, pool, uuid.MustParse(ws), "k1")

	inserted, err := store.TryInsert(ctx, ws, "k1", []byte("hash-2"))
	if err != nil {
		t.Fatalf("reclaiming TryInsert: %v", err)
	}
	if !inserted {
		t.Fatal("an expired row was not reclaimed")
	}

	rec, found, err := store.Get(ctx, ws, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("reclaimed row not found")
	}
	if string(rec.RequestHash) != "hash-2" {
		t.Errorf("RequestHash = %q, want the reclaiming request's hash-2", rec.RequestHash)
	}
	if rec.StatusCode != nil {
		t.Errorf("StatusCode = %d, want nil (reset by the reclaim)", *rec.StatusCode)
	}
}

// GetIdempotencyKey's own age filter treats a matching-but-25h-old row as
// absent, even though nothing has physically deleted it yet (the maintenance
// sweep has not run).
func TestGetIgnoresA25HourOldRowEvenBeforeTheSweepRuns(t *testing.T) {
	ctx := context.Background()
	pool, store := setup(t)
	ws := uuid.New().String()

	if inserted, err := store.TryInsert(ctx, ws, "k1", []byte("hash-1")); err != nil || !inserted {
		t.Fatalf("TryInsert: inserted=%v err=%v", inserted, err)
	}
	backdate(t, ctx, pool, uuid.MustParse(ws), "k1")

	_, found, err := store.Get(ctx, ws, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("a 25h-old row was returned as if it were live")
	}
}

// Delete actually removes the row, not just logically hides it -- a subsequent
// Get must report not-found.
func TestDeleteRemovesTheRow(t *testing.T) {
	ctx := context.Background()
	_, store := setup(t)
	ws := uuid.New().String()

	if inserted, err := store.TryInsert(ctx, ws, "k1", []byte("hash-1")); err != nil || !inserted {
		t.Fatalf("TryInsert: inserted=%v err=%v", inserted, err)
	}
	if err := store.Delete(ctx, ws, "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, found, err := store.Get(ctx, ws, "k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("row still found after Delete")
	}
}

// The primary key is (workspace_id, key): a row inserted under workspace A is
// invisible to Get under workspace B even though the key string matches exactly.
func TestGetIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	_, store := setup(t)
	wsA, wsB := uuid.New().String(), uuid.New().String()

	if inserted, err := store.TryInsert(ctx, wsA, "k", []byte("hash-1")); err != nil || !inserted {
		t.Fatalf("TryInsert: inserted=%v err=%v", inserted, err)
	}

	_, found, err := store.Get(ctx, wsB, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("workspace B saw workspace A's key even though the key string matches")
	}
}
