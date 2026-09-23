//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// The retention age indexes, each built by its own one-statement CONCURRENTLY
// migration (20260923144743..48).
var retentionIndexes = []string{
	"idx_tracking_events_created_at", "idx_deliverability_events_received_at",
	"idx_inbox_threads_last_message_at", "idx_sends_created_at",
	"idx_inbox_threads_campaign_contact", "idx_inbox_pending_replies_thread",
}

// The retention migrations go up, down and up again cleanly on a database of
// their own (see dbtest.ScratchDSN for why a rollback never runs on the shared
// one). This is also the proof the CONCURRENTLY files rely on: golang-migrate's
// pgx/v5 driver sends a file as one Exec, so a one-statement file is NOT in a
// transaction block — were it, Postgres would refuse the build outright
// ("CREATE INDEX CONCURRENTLY cannot run inside a transaction block"). And a
// concurrent build that "succeeded" but left an INVALID index would be the worst
// outcome, so every index is asserted valid, not merely present.
func TestRetentionMigrationsRollBackAndForwardAgain(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "retention_migration")

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up (a CONCURRENTLY file inside a transaction block would fail here): %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close() // before the scratch DROP, which the t.Cleanup above runs after

	assertRetentionSchema(t, ctx, pool, true)

	// The version before the first retention migration.
	if err := db.MigrateTo(dsn, 20260921111415); err != nil {
		t.Fatalf("migrate down to 20260921111415: %v", err)
	}
	assertRetentionSchema(t, ctx, pool, false)

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	assertRetentionSchema(t, ctx, pool, true)
}

func assertRetentionSchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool, up bool) {
	t.Helper()
	var tables, views, validIndexes, invalidIndexes, oldSendIndex, tunedTables int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename IN ('tracking_event_rollups', 'retention_cursors')),
		  (SELECT count(*) FROM pg_views WHERE schemaname = 'public' AND viewname = 'tracking_engagement'),
		  (SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = ANY($1) AND i.indisvalid),
		  (SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = ANY($1) AND NOT i.indisvalid),
		  (SELECT count(*) FROM pg_class WHERE relname = 'idx_tracking_events_send' AND relkind = 'i'),
		  (SELECT count(*) FROM pg_class WHERE relname IN ('sends', 'tracking_events', 'deliverability_events')
		     AND reloptions @> ARRAY['autovacuum_vacuum_scale_factor=0.01', 'autovacuum_vacuum_threshold=50000'])`,
		retentionIndexes).Scan(&tables, &views, &validIndexes, &invalidIndexes, &oldSendIndex, &tunedTables); err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if invalidIndexes != 0 {
		t.Fatalf("%d retention index(es) are INVALID — a concurrent build failed and left a broken index behind", invalidIndexes)
	}
	if up {
		if tables != 2 || views != 1 || validIndexes != len(retentionIndexes) || oldSendIndex != 0 || tunedTables != 3 {
			t.Fatalf("after up: tables=%d view=%d valid indexes=%d/%d old send index=%d tuned tables=%d; want 2/1/%d/0/3",
				tables, views, validIndexes, len(retentionIndexes), oldSendIndex, tunedTables, len(retentionIndexes))
		}
		return
	}
	if tables != 0 || views != 0 || validIndexes != 0 || oldSendIndex != 1 || tunedTables != 0 {
		t.Fatalf("after down: tables=%d view=%d indexes=%d old send index=%d tuned tables=%d; want 0/0/0/1 (restored)/0",
			tables, views, validIndexes, oldSendIndex, tunedTables)
	}
}
