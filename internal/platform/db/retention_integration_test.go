//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// 20260923110315_recipient_data_retention must go up, down and up again cleanly,
// on a database of its own (see dbtest.ScratchDSN for why a rollback never runs
// on the shared one). Down is named as the VERSION before it rather than as
// "one step", so a later migration landing on top cannot make this test roll
// back the wrong file.
func TestRetentionMigrationRollsBackAndForwardAgain(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "retention_migration")

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close() // before the scratch DROP, which the t.Cleanup above runs after

	objects := func() (tables, views, indexes int) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT
			  (SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename = 'tracking_event_rollups'),
			  (SELECT count(*) FROM pg_views  WHERE schemaname = 'public' AND viewname  = 'tracking_engagement'),
			  (SELECT count(*) FROM pg_indexes WHERE schemaname = 'public' AND indexname = ANY($1))`,
			[]string{
				"idx_tracking_events_created_at", "idx_deliverability_events_received_at",
				"idx_inbox_threads_last_message_at", "idx_sends_created_at", "idx_inbox_threads_campaign_contact",
			}).Scan(&tables, &views, &indexes); err != nil {
			t.Fatalf("read catalog: %v", err)
		}
		return tables, views, indexes
	}

	if tb, v, ix := objects(); tb != 1 || v != 1 || ix != 5 {
		t.Fatalf("after up: rollups table=%d view=%d age indexes=%d, want 1/1/5", tb, v, ix)
	}

	if err := db.MigrateTo(dsn, 20260921111415); err != nil {
		t.Fatalf("migrate down to 20260921111415: %v", err)
	}
	if tb, v, ix := objects(); tb != 0 || v != 0 || ix != 0 {
		t.Fatalf("after down: rollups table=%d view=%d age indexes=%d, want all gone", tb, v, ix)
	}

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if tb, v, ix := objects(); tb != 1 || v != 1 || ix != 5 {
		t.Fatalf("after up again: rollups table=%d view=%d age indexes=%d, want 1/1/5", tb, v, ix)
	}
}
