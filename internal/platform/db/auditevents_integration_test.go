//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// The audit_events migration must round-trip: up, down to the version before
// it, and up again, leaving the table, its trigger function and both triggers
// exactly as a fresh install would have them. Named-version rollback
// (MigrateTo) so this keeps testing THIS migration after later ones land.
func TestAuditEventsMigrationRoundTrips(t *testing.T) {
	const (
		auditVersion   = 20260923105934
		cascadeVersion = 20260923144934
		before         = 20260921111415
	)
	dsn := dbtest.ScratchDSN(t, "audit_events")
	ctx := context.Background()

	count := func(label, q string) int {
		t.Helper()
		pool, err := db.Connect(ctx, dsn)
		if err != nil {
			t.Fatalf("%s: connect: %v", label, err)
		}
		defer pool.Close()
		var n int
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return n
	}
	objects := func(label string) int {
		return count(label, `
			SELECT (SELECT count(*) FROM pg_class WHERE relname = 'audit_events')
			     + (SELECT count(*) FROM pg_proc WHERE proname = 'audit_events_append_only')
			     + (SELECT count(*) FROM pg_trigger WHERE tgname IN ('trg_audit_events_append_only', 'trg_audit_events_no_truncate'))`)
	}

	if err := db.MigrateTo(dsn, auditVersion); err != nil {
		t.Fatalf("up: %v", err)
	}
	if n := objects("after up"); n != 4 {
		t.Fatalf("after up: %d of 4 audit objects present", n)
	}
	if err := db.MigrateTo(dsn, before); err != nil {
		t.Fatalf("down: %v", err)
	}
	if n := objects("after down"); n != 0 {
		t.Fatalf("after down: %d audit objects left behind", n)
	}
	if err := db.MigrateTo(dsn, auditVersion); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if n := objects("after up again"); n != 4 {
		t.Fatalf("after up again: %d of 4 audit objects present", n)
	}

	// The cascade-guard migration replaces the function body; its down must
	// restore the original exactly, and up must re-apply the guard.
	guarded := func(label string) bool {
		return count(label, `SELECT count(*) FROM pg_proc WHERE proname = 'audit_events_append_only' AND prosrc LIKE '%NOT EXISTS%'`) == 1
	}
	for _, step := range []struct {
		version uint
		want    bool
	}{{cascadeVersion, true}, {auditVersion, false}, {cascadeVersion, true}} {
		if err := db.MigrateTo(dsn, step.version); err != nil {
			t.Fatalf("migrate to %d: %v", step.version, err)
		}
		if got := guarded("cascade guard"); got != step.want {
			t.Fatalf("at %d: cascade guard present = %v, want %v", step.version, got, step.want)
		}
	}
}
