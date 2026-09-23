//go:build integration

package inprocess

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPurgeAuditEventsDeletesOnlyPastRetention(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	ws, err := q.CreateWorkspace(ctx, "audit retention IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	oldID, freshID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_events (id, workspace_id, actor_type, action, created_at) VALUES
			($1, $3, 'system', 'auth.login', now() - interval '400 days'),
			($2, $3, 'system', 'auth.login', now() - interval '10 days')`,
		oldID, freshID, ws.ID); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c := client{pool: pool, q: q}

	exists := func(id uuid.UUID) bool {
		t.Helper()
		var ok bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events WHERE id = $1)`, id).Scan(&ok); err != nil {
			t.Fatalf("exists: %v", err)
		}
		return ok
	}

	// Disabled retention deletes nothing, however old the row.
	if n, err := c.PurgeAuditEvents(ctx, 0); err != nil || n != 0 || !exists(oldID) {
		t.Fatalf("retention 0: deleted %d (%v), old row present %v", n, err, exists(oldID))
	}

	n, err := c.PurgeAuditEvents(ctx, 365)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n < 1 || exists(oldID) {
		t.Fatalf("deleted %d, old row still present = %v", n, exists(oldID))
	}
	if !exists(freshID) {
		t.Fatal("purge deleted a row inside the retention window")
	}

	// The purge's own door closes with its transaction: an ordinary DELETE
	// afterwards is refused again.
	if _, err := pool.Exec(ctx, `DELETE FROM audit_events WHERE id = $1`, freshID); err == nil {
		t.Fatal("DELETE succeeded after the purge committed; the retention door leaked")
	}
}
