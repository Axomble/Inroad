//go:build integration

package inprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These integration tests exercise the worker-routing assigner (migration
// 000018) directly against Postgres: no-live-worker fallback, least-loaded pick,
// idempotency, and workspace pinning. Docker must be up (same harness as
// claim_integration_test.go).

// routingClient builds a minimal inprocess client — the routing methods use only
// c.q / c.pool, so no keyring/oauth wiring is needed here.
func routingClient(pool *pgxpool.Pool, q *gen.Queries) client {
	return client{pool: pool, q: q}
}

func createRoutingMailbox(t *testing.T, ctx context.Context, q *gen.Queries, ws uuid.UUID) uuid.UUID {
	t.Helper()
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: "mb-" + uuid.NewString() + "@x.test", DisplayName: "MB",
		SmtpHost: "smtp.x.test", SmtpPort: 587, SmtpUsername: "u",
		ImapHost: "imap.x.test", ImapPort: 993, ImapUsername: "u",
		SecretCiphertext: "ciphertext", DailyCap: 100, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	return mb.ID
}

// TestAssignMailboxWorkerNoLiveWorkerFallback: with no live heartbeat the
// assigner returns "" (shared default queue) and persists nothing, so a real
// worker can claim the mailbox once it comes online (single-node dev).
func TestAssignMailboxWorkerNoLiveWorkerFallback(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	ws, err := q.CreateWorkspace(ctx, "Routing "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)

	queueName, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if queueName != "" {
		t.Fatalf("no live worker must return the default queue \"\", got %q", queueName)
	}
	// Nothing persisted: the next call (once a worker is live) makes a fresh pick.
	if _, err := q.GetMailboxWorkerAssignment(ctx, gen.GetMailboxWorkerAssignmentParams{
		MailboxID: mb, WorkspaceID: ws.ID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("fallback must not persist an assignment, got err=%v", err)
	}
}

// TestAssignMailboxWorkerLeastLoadedAndIdempotent: the assigner picks the
// least-loaded live worker, then returns that same assignment on every
// subsequent call (idempotent), even after the load balance changes.
func TestAssignMailboxWorkerLeastLoadedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	ws, err := q.CreateWorkspace(ctx, "Routing "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	// Two live workers. "aaa" sorts first, so it would win a zero-zero tie — we
	// pre-load it so "bbb" is strictly least-loaded and the pick can't be the
	// accidental tie-break winner.
	if err := c.UpsertWorkerHeartbeat(ctx, "aaa", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat aaa: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "bbb", "203.0.113.2"); err != nil {
		t.Fatalf("heartbeat bbb: %v", err)
	}
	// Load "aaa" with two existing assignments (real mailboxes for the FK).
	for i := 0; i < 2; i++ {
		load := createRoutingMailbox(t, ctx, q, ws.ID)
		if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
			MailboxID: load, WorkspaceID: ws.ID, WorkerID: "aaa",
		}); err != nil {
			t.Fatalf("preload aaa: %v", err)
		}
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if got != "w:bbb" {
		t.Fatalf("least-loaded pick = %q, want w:bbb", got)
	}

	// Idempotent: repeated calls return the SAME assignment even though "bbb" is
	// now more loaded than "aaa" was — an existing assignment is never re-picked.
	for i := 0; i < 3; i++ {
		again, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
		if err != nil {
			t.Fatalf("re-assign %d: %v", i, err)
		}
		if again != "w:bbb" {
			t.Fatalf("re-assign %d = %q, want stable w:bbb", i, again)
		}
	}
}

// TestAssignMailboxWorkerWorkspacePinning: an assignment is invisible to another
// workspace — a foreign workspace_id reads zero rows, upholding the tenant pin
// on mailbox_worker_assignments (spec §17.9).
func TestAssignMailboxWorkerWorkspacePinning(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)

	ws, err := q.CreateWorkspace(ctx, "Routing owner "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	foreign, err := q.CreateWorkspace(ctx, "Routing foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "aaa", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil {
		t.Fatalf("assign under owner: %v", err)
	}

	// The foreign workspace cannot READ the owner's assignment.
	if _, err := q.GetMailboxWorkerAssignment(ctx, gen.GetMailboxWorkerAssignmentParams{
		MailboxID: mb, WorkspaceID: foreign.ID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign workspace must read zero assignment rows, got err=%v", err)
	}

	// The owner still resolves it idempotently.
	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil || got != "w:aaa" {
		t.Fatalf("owner re-assign = %q err=%v, want w:aaa", got, err)
	}
}
