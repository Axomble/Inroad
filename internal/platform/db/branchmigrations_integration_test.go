//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// The two conditional-branching migrations, in order.
const (
	branchTablesVersion = 20260923110214
	branchIndexVersion  = 20260923144758
	// preBranchVersion is the migration immediately before the branching tables.
	preBranchVersion = 20260921111415
)

// indexState reports whether the named index exists and, if so, whether it is
// VALID — a failed CREATE INDEX CONCURRENTLY leaves an invalid index behind
// rather than rolling back, and IF NOT EXISTS would skip it on every re-run.
func indexState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) (exists, valid bool) {
	t.Helper()
	err := pool.QueryRow(ctx, `
		SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = $1`, name).Scan(&valid)
	if err != nil {
		return false, false
	}
	return true, valid
}

// On a FRESH database, so the concurrent build actually runs rather than being
// skipped by IF NOT EXISTS: the migration applies (proving the golang-migrate
// pgx/v5 driver runs a single-statement file outside a transaction block, which
// CREATE INDEX CONCURRENTLY requires), the index is valid, and both branching
// migrations round-trip down and up again.
func TestInboxThreadsCampaignContactIndexIsValid(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "branch_index")

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up (fresh): %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const idx = "idx_inbox_threads_campaign_contact"
	if exists, valid := indexState(t, ctx, pool, idx); !exists || !valid {
		t.Fatalf("%s after a fresh migrate: exists=%v valid=%v", idx, exists, valid)
	}

	// Down past both branching migrations, then up again.
	if err := db.MigrateTo(dsn, branchIndexVersion); err != nil {
		t.Fatalf("migrate to %d: %v", branchIndexVersion, err)
	}
	if err := db.MigrateTo(dsn, branchTablesVersion); err != nil {
		t.Fatalf("roll back the index migration: %v", err)
	}
	if exists, _ := indexState(t, ctx, pool, idx); exists {
		t.Fatalf("%s survived its own down migration", idx)
	}
	if err := db.MigrateTo(dsn, preBranchVersion); err != nil {
		t.Fatalf("roll back the branch tables: %v", err)
	}
	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname = 'sequence_step_branches'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("sequence_step_branches after down: count=%d err=%v", tables, err)
	}
	var col int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'sequence_enrollments' AND column_name = 'awaiting_condition_step'`).Scan(&col); err != nil || col != 0 {
		t.Fatalf("awaiting_condition_step after down: count=%d err=%v", col, err)
	}

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if exists, valid := indexState(t, ctx, pool, idx); !exists || !valid {
		t.Fatalf("%s after up/down/up: exists=%v valid=%v", idx, exists, valid)
	}
}
