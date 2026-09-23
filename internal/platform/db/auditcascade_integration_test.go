//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// The append-only trigger's cascade exception must admit exactly one thing:
// the ON DELETE CASCADE from deleting the audit row's own workspace. A delete
// issued from inside SOME OTHER trigger while the workspace still exists is
// refused like any direct DELETE (migration 20260923144934).
//
// Runs on a scratch database because it installs a probe trigger, which must
// never exist in the shared test schema.
func TestAuditCascadeExceptionAdmitsOnlyAWorkspaceDelete(t *testing.T) {
	dsn := dbtest.ScratchDSN(t, "audit_cascade")
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	newWorkspaceWithEvent := func() uuid.UUID {
		t.Helper()
		var ws uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO workspaces (name) VALUES ('cascade-it') RETURNING id`).Scan(&ws); err != nil {
			t.Fatalf("workspace: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO audit_events (workspace_id, actor_type, action) VALUES ($1, 'system', 'auth.login')`, ws); err != nil {
			t.Fatalf("audit row: %v", err)
		}
		return ws
	}
	auditRows := func(ws uuid.UUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1`, ws).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// A trigger elsewhere that deletes a LIVE workspace's audit rows: exactly
	// the depth-2 delete the old `pg_trigger_depth() > 1` rule let through.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE audit_probe (workspace_id UUID NOT NULL);
		CREATE FUNCTION audit_probe_delete() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			DELETE FROM audit_events WHERE workspace_id = NEW.workspace_id;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER trg_audit_probe AFTER INSERT ON audit_probe
			FOR EACH ROW EXECUTE FUNCTION audit_probe_delete();`); err != nil {
		t.Fatalf("install probe: %v", err)
	}

	live := newWorkspaceWithEvent()
	_, err = pool.Exec(ctx, `INSERT INTO audit_probe (workspace_id) VALUES ($1)`, live)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("trigger-issued delete of a live workspace's audit rows: err = %v, want the append-only refusal", err)
	}
	if n := auditRows(live); n != 1 {
		t.Fatalf("audit rows after the refused trigger delete = %d, want 1", n)
	}

	// The workspace's own cascade still goes through.
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, live); err != nil {
		t.Fatalf("workspace delete blocked by the audit trigger: %v", err)
	}
	if n := auditRows(live); n != 0 {
		t.Fatalf("audit rows after workspace delete = %d, want 0", n)
	}
}
