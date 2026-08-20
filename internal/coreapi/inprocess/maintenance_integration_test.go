//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCleanupExpiredPurgesOnlyOutsideRetentionWindow(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)

	ws, err := q.CreateWorkspace(ctx, "Retention IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash) VALUES ($1, 'test-only') RETURNING id`,
		"retention-"+uuid.NewString()+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')`,
		ws.ID, userID); err != nil {
		t.Fatalf("membership: %v", err)
	}

	expiredID, freshID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, workspace_id, token_hash, family_id, expires_at)
		VALUES
			($1, $2, $3, $4, $5, $6),
			($7, $2, $3, $8, $9, $10)`,
		expiredID, userID, ws.ID, []byte("expired-"+uuid.NewString()), uuid.New(), time.Now().Add(-31*24*time.Hour),
		freshID, []byte("fresh-"+uuid.NewString()), uuid.New(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("sessions: %v", err)
	}

	deleted, err := (client{q: q}).CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("deleted rows = %d, want at least 1", deleted)
	}

	var expiredExists, freshExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1),
		       EXISTS(SELECT 1 FROM sessions WHERE id = $2)`, expiredID, freshID).
		Scan(&expiredExists, &freshExists); err != nil {
		t.Fatalf("verify sessions: %v", err)
	}
	if expiredExists || !freshExists {
		t.Fatalf("cleanup result: expired_exists=%t fresh_exists=%t", expiredExists, freshExists)
	}
}

// TestPurgeDeadWorkersReapsRegistryAndAssignments covers the reaper half of the
// dead-worker work (GH #114). It is not what keeps mail flowing — the assigner
// already routes around a dead worker — it is what stops dead rows permanently
// inflating a departed worker's load in the fleet-wide least-loaded pick.
//
// The 24h retention is deliberately much wider than the assigner's 15m live
// window, so the interesting boundary is "dead to the assigner but NOT yet
// reapable": that worker's rows must survive.
func TestPurgeDeadWorkersReapsRegistryAndAssignments(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Worker purge "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	// Three workers spanning both windows, each holding one real assignment (the
	// mailbox FK requires a real row).
	const longDead, recentlyDead, alive = "purge-long-dead", "purge-recently-dead", "purge-alive"
	ages := map[string]string{
		longDead:     "25 hours",   // past the 24h retention: reapable
		recentlyDead: "30 minutes", // past the 15m live window but NOT reapable
		alive:        "1 minute",
	}
	mailboxes := map[string]uuid.UUID{}
	for workerID, age := range ages {
		if _, err := pool.Exec(ctx,
			"INSERT INTO workers (worker_id, egress_ip, last_seen_at) VALUES ($1, '203.0.113.1', now() - $2::interval)",
			workerID, age); err != nil {
			t.Fatalf("seed worker %s: %v", workerID, err)
		}
		mb := createRoutingMailbox(t, ctx, q, ws.ID)
		mailboxes[workerID] = mb
		if _, err := pool.Exec(ctx,
			"INSERT INTO mailbox_worker_assignments (mailbox_id, workspace_id, worker_id) VALUES ($1, $2, $3)",
			mb, ws.ID, workerID); err != nil {
			t.Fatalf("seed assignment for %s: %v", workerID, err)
		}
	}

	// One worker row plus one assignment row.
	const wantDeleted = 2
	deleted, err := (client{q: q}).PurgeDeadWorkers(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != wantDeleted {
		t.Fatalf("deleted rows = %d, want %d (one worker + its one assignment)", deleted, wantDeleted)
	}

	for workerID := range ages {
		var workerExists, assignmentExists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM workers WHERE worker_id = $1),
			       EXISTS(SELECT 1 FROM mailbox_worker_assignments WHERE mailbox_id = $2)`,
			workerID, mailboxes[workerID]).Scan(&workerExists, &assignmentExists); err != nil {
			t.Fatalf("verify %s: %v", workerID, err)
		}
		wantExists := workerID != longDead
		if workerExists != wantExists || assignmentExists != wantExists {
			t.Fatalf("%s: worker_exists=%t assignment_exists=%t, want both %t",
				workerID, workerExists, assignmentExists, wantExists)
		}
	}

	// Idempotent: nothing is left in window, so a second sweep is a no-op rather
	// than an error or a double count. The cleanup job runs on a schedule, so this
	// is its steady state, not an edge case.
	if deleted, err := (client{q: q}).PurgeDeadWorkers(ctx); err != nil || deleted != 0 {
		t.Fatalf("second purge: deleted=%d err=%v, want 0 and no error", deleted, err)
	}

	// The surviving mailboxes still exist: the purge reaps routing rows, never the
	// tenant data they point at.
	var mailboxCount int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM mailboxes WHERE workspace_id = $1", ws.ID).Scan(&mailboxCount); err != nil {
		t.Fatalf("count mailboxes: %v", err)
	}
	if mailboxCount != len(ages) {
		t.Fatalf("mailboxes = %d, want %d — the purge must not touch tenant data", mailboxCount, len(ages))
	}
}

func TestPurgeIdempotencyKeysPurgesOnlyOutsideRetentionWindow(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)

	ws := uuid.New() // idempotency_keys carries no FK to workspaces; any uuid pins tenancy here.
	expiredKey, freshKey := "expired-"+uuid.NewString(), "fresh-"+uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO idempotency_keys (workspace_id, key, request_hash, created_at)
		VALUES
			($1, $2, $3, now() - interval '25 hours'),
			($1, $4, $3, now() - interval '1 hour')`,
		ws, expiredKey, []byte("hash"), freshKey); err != nil {
		t.Fatalf("seed idempotency_keys: %v", err)
	}

	deleted, err := (client{q: q}).PurgeIdempotencyKeys(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("deleted rows = %d, want at least 1", deleted)
	}

	var expiredExists, freshExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM idempotency_keys WHERE workspace_id = $1 AND key = $2),
		       EXISTS(SELECT 1 FROM idempotency_keys WHERE workspace_id = $1 AND key = $3)`,
		ws, expiredKey, freshKey).Scan(&expiredExists, &freshExists); err != nil {
		t.Fatalf("verify idempotency_keys: %v", err)
	}
	if expiredExists || !freshExists {
		t.Fatalf("purge result: expired_exists=%t fresh_exists=%t", expiredExists, freshExists)
	}
}
