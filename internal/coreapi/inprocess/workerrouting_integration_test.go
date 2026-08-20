//go:build integration

package inprocess

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These integration tests exercise the worker-routing assigner (migration
// 000017) directly against Postgres: no-live-worker fallback, least-loaded pick,
// idempotency, workspace pinning, self-enforcing write tenancy, and — the
// deploy-safety half — dead-worker liveness and reassignment. Docker must be up
// (same harness as claim_integration_test.go).

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

// liveSinceNow is the live-worker cutoff the assigner itself computes, for tests
// that call a generated query directly. Derived from workerLiveWindow rather than
// a literal so widening that window cannot silently invalidate these tests.
func liveSinceNow() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().Add(-workerLiveWindow), Valid: true}
}

// killWorker ages a worker's heartbeat past the live window, simulating the state
// a rolling deploy leaves behind: the process is gone but its rows are not. The
// row is aged rather than deleted because that is the strictly harder case — a
// deleted worker fails the liveness join trivially, whereas a stale one still
// joins and must be excluded on last_seen_at.
func killWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workerID string, age time.Duration) {
	t.Helper()
	tag, err := pool.Exec(ctx,
		"UPDATE workers SET last_seen_at = now() - $2::interval WHERE worker_id = $1",
		workerID, fmt.Sprintf("%d seconds", int64(age.Seconds())))
	if err != nil {
		t.Fatalf("age worker %s: %v", workerID, err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("age worker %s: updated %d rows, want 1", workerID, tag.RowsAffected())
	}
}

// storedAssignment reads the persisted routing row for a (mailbox, workspace)
// pair, reporting whether one exists at all and which worker owns it.
//
// Deliberately raw SQL rather than a generated query: the only reader query,
// GetLiveMailboxWorkerAssignment, hides a row whose worker has gone silent, and
// several tests below need to distinguish "no row was written" from "a row was
// written but points at a dead worker". Asserting on the table itself also keeps
// these tests honest about the second half of the reassignment contract — that
// the row moves IN PLACE rather than a duplicate appearing — which no query
// exposes.
func storedAssignment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mailbox, ws uuid.UUID) (workerID string, exists bool) {
	t.Helper()
	rows, err := pool.Query(ctx,
		"SELECT worker_id FROM mailbox_worker_assignments WHERE mailbox_id = $1 AND workspace_id = $2",
		mailbox, ws)
	if err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			t.Fatalf("scan assignment: %v", err)
		}
		found = append(found, w)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate assignments: %v", err)
	}
	// mailbox_id is the primary key, so more than one row is impossible — assert it
	// anyway: an upsert rewritten into a DELETE+INSERT (or a conflict target moved
	// off the PK) would show up here first.
	if len(found) > 1 {
		t.Fatalf("mailbox %s has %d assignment rows, want at most 1", mailbox, len(found))
	}
	if len(found) == 0 {
		return "", false
	}
	return found[0], true
}

// resetRouting clears the two GLOBAL-infra routing tables. Unlike tenant data
// (isolated per test by unique workspace/mailbox UUIDs), `workers` and
// `mailbox_worker_assignments` use fixed worker ids that otherwise leak across
// tests sharing this Postgres — a stale live worker would be picked by the
// fleet-wide least-loaded query and make assignment order non-deterministic.
func resetRouting(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, "TRUNCATE mailbox_worker_assignments, workers"); err != nil {
		t.Fatalf("reset routing tables: %v", err)
	}
}

// TestAssignMailboxWorkerNoLiveWorkerFallback: with no live heartbeat the
// assigner returns "" (shared default queue) and persists nothing, so a real
// worker can claim the mailbox once it comes online (single-node dev).
func TestAssignMailboxWorkerNoLiveWorkerFallback(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

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
	if worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID); exists {
		t.Fatalf("fallback must not persist an assignment, got worker_id=%q", worker)
	}
}

// TestAssignMailboxWorkerLeastLoadedAndIdempotent: the assigner picks the
// least-loaded live worker, then returns that same assignment on every
// subsequent call (idempotent), even after the load balance changes.
func TestAssignMailboxWorkerLeastLoadedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

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
			MailboxID: load, WorkspaceID: ws.ID, WorkerID: "aaa", LiveSince: liveSinceNow(),
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
	resetRouting(t, ctx, pool)

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

	// The foreign workspace cannot READ the owner's assignment. "aaa" is live, so a
	// zero-row result here is the workspace pin, not the liveness join.
	if _, err := q.GetLiveMailboxWorkerAssignment(ctx, gen.GetLiveMailboxWorkerAssignmentParams{
		MailboxID: mb, WorkspaceID: foreign.ID, LiveSince: liveSinceNow(),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign workspace must read zero assignment rows, got err=%v", err)
	}

	// The owner still resolves it idempotently.
	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil || got != "w:aaa" {
		t.Fatalf("owner re-assign = %q err=%v, want w:aaa", got, err)
	}
}

// TestAssignMailboxWorkerWriteTenancy: with a LIVE worker available (so the pick
// succeeds and the assigner reaches the persist step), assigning a mailbox under a
// NON-owning workspace_id inserts zero rows and returns ErrCrossTenant, persisting
// nothing — distinct from the no-live-worker fallback (which never reaches the
// insert). The owner then still assigns normally. This proves the write path is
// self-enforcing, not just the read pin (spec §17.9, defense in depth).
func TestAssignMailboxWorkerWriteTenancy(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	owner, err := q.CreateWorkspace(ctx, "Routing owner "+uuid.NewString())
	if err != nil {
		t.Fatalf("owner workspace: %v", err)
	}
	foreign, err := q.CreateWorkspace(ctx, "Routing foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	// A live worker so PickLeastLoadedWorker returns a row and control reaches the
	// self-enforcing insert (otherwise the no-live-worker branch short-circuits).
	if err := c.UpsertWorkerHeartbeat(ctx, "aaa", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, owner.ID)

	// Foreign workspace claiming the owner's mailbox: the INSERT ... SELECT matches
	// zero mailbox rows, RETURNING yields ErrNoRows, mapped to ErrCrossTenant.
	if _, err := c.AssignMailboxWorker(ctx, mb.String(), foreign.ID.String()); !errors.Is(err, coreapi.ErrCrossTenant) {
		t.Fatalf("foreign-workspace assign err = %v, want ErrCrossTenant", err)
	}
	// Nothing persisted: the mailbox has no assignment row under EITHER workspace.
	if worker, exists := storedAssignment(t, ctx, pool, mb, owner.ID); exists {
		t.Fatalf("rejected cross-tenant assign must persist nothing, got worker_id=%q", worker)
	}
	if worker, exists := storedAssignment(t, ctx, pool, mb, foreign.ID); exists {
		t.Fatalf("rejected cross-tenant assign must persist nothing, got worker_id=%q", worker)
	}

	// The legitimate owner still assigns to the live worker.
	got, err := c.AssignMailboxWorker(ctx, mb.String(), owner.ID.String())
	if err != nil || got != "w:aaa" {
		t.Fatalf("owner assign = %q err=%v, want w:aaa", got, err)
	}
}

// TestAssignMailboxWorkerKeepsLiveIncumbent: an assignment whose worker is still
// heartbeating is returned unchanged, and no write happens — including when a
// strictly less-loaded worker exists. This is the pre-existing pin that makes a
// mailbox's mail egress from one IP; the liveness work must not turn every
// resolve into a rebalance.
func TestAssignMailboxWorkerKeepsLiveIncumbent(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing live "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "aaa", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat aaa: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:aaa" {
		t.Fatalf("first assign = %q err=%v, want w:aaa", got, err)
	}
	var firstAssignedAt time.Time
	if err := pool.QueryRow(ctx,
		"SELECT assigned_at FROM mailbox_worker_assignments WHERE mailbox_id = $1", mb).
		Scan(&firstAssignedAt); err != nil {
		t.Fatalf("read assigned_at: %v", err)
	}

	// A brand-new, completely unloaded worker joins. "aaa" is now the MORE loaded
	// of the two, so a resolve that re-picked would move the mailbox to "bbb".
	if err := c.UpsertWorkerHeartbeat(ctx, "bbb", "203.0.113.2"); err != nil {
		t.Fatalf("heartbeat bbb: %v", err)
	}
	for i := range 3 {
		got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
		if err != nil || got != "w:aaa" {
			t.Fatalf("resolve %d = %q err=%v, want stable w:aaa", i, got, err)
		}
	}

	worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID)
	if !exists || worker != "aaa" {
		t.Fatalf("stored assignment = (%q, exists=%t), want (aaa, true)", worker, exists)
	}
	// assigned_at untouched: a live incumbent's row is not rewritten, so the age of
	// the IP pin (what deliverability reasoning depends on) survives every resolve.
	var nowAssignedAt time.Time
	if err := pool.QueryRow(ctx,
		"SELECT assigned_at FROM mailbox_worker_assignments WHERE mailbox_id = $1", mb).
		Scan(&nowAssignedAt); err != nil {
		t.Fatalf("re-read assigned_at: %v", err)
	}
	if !nowAssignedAt.Equal(firstAssignedAt) {
		t.Fatalf("assigned_at moved for a live incumbent: %s -> %s", firstAssignedAt, nowAssignedAt)
	}
}

// TestAssignMailboxWorkerReassignsAwayFromDeadWorker is the deploy-safety case
// (GH #114). A worker that stops heartbeating leaves its mailboxes pinned to
// "w:<dead-id>" — a queue no process consumes, so tasks neither run nor fail nor
// alert and the mailbox silently goes quiet. Every rolling deploy under a
// scheduler that changes instance identity produces exactly that state.
//
// The assertions are the whole contract: the stale assignment is not returned,
// the mailbox lands on a LIVE worker, the existing row is UPDATED in place (no
// duplicate, and assigned_at is reset because the pin genuinely moved), and the
// new answer is then itself stable.
func TestAssignMailboxWorkerReassignsAwayFromDeadWorker(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing dead "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "old-node", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat old-node: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:old-node" {
		t.Fatalf("initial assign = %q err=%v, want w:old-node", got, err)
	}
	var deadAssignedAt time.Time
	if err := pool.QueryRow(ctx,
		"SELECT assigned_at FROM mailbox_worker_assignments WHERE mailbox_id = $1", mb).
		Scan(&deadAssignedAt); err != nil {
		t.Fatalf("read assigned_at: %v", err)
	}

	// The deploy: old-node's heartbeat stops (aged just past the live window, the
	// boundary case) and new-node comes up under a different id.
	killWorker(t, ctx, pool, "old-node", workerLiveWindow+time.Minute)
	if err := c.UpsertWorkerHeartbeat(ctx, "new-node", "203.0.113.2"); err != nil {
		t.Fatalf("heartbeat new-node: %v", err)
	}

	// The stale row is invisible to the reader, so the assigner re-picks. Assert on
	// the query too: this is the join that decides whether mail keeps flowing.
	if _, err := q.GetLiveMailboxWorkerAssignment(ctx, gen.GetLiveMailboxWorkerAssignmentParams{
		MailboxID: mb, WorkspaceID: ws.ID, LiveSince: liveSinceNow(),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a dead worker's assignment must read as absent, got err=%v", err)
	}

	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if got != "w:new-node" {
		t.Fatalf("reassign = %q, want w:new-node (stranded on a dead worker's queue)", got)
	}

	// Updated in place: exactly one row, now owned by the live worker.
	// storedAssignment fails the test if a duplicate appeared.
	worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID)
	if !exists {
		t.Fatal("reassignment must leave a row, got none")
	}
	if worker != "new-node" {
		t.Fatalf("stored worker_id = %q, want new-node — the row did not move", worker)
	}
	// assigned_at reset: unlike the live-incumbent path, the pin really did move, so
	// its age must reflect the new worker rather than the dead one.
	var movedAssignedAt time.Time
	if err := pool.QueryRow(ctx,
		"SELECT assigned_at FROM mailbox_worker_assignments WHERE mailbox_id = $1", mb).
		Scan(&movedAssignedAt); err != nil {
		t.Fatalf("re-read assigned_at: %v", err)
	}
	if !movedAssignedAt.After(deadAssignedAt) {
		t.Fatalf("assigned_at not reset on reassignment: %s -> %s", deadAssignedAt, movedAssignedAt)
	}

	// The new assignment is itself idempotent — reassignment is a one-shot move, not
	// a per-resolve rebalance.
	if again, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || again != "w:new-node" {
		t.Fatalf("post-reassign resolve = %q err=%v, want stable w:new-node", again, err)
	}
}

// TestAssignMailboxWorkerWithinLiveWindowIsNotReassigned guards the other side of
// the boundary: a worker that has merely MISSED a heartbeat tick (5m interval, 15m
// window) is still live, so its mailboxes stay put. Without this, the reassignment
// rule above would happily churn the whole fleet's pins on a slow tick.
func TestAssignMailboxWorkerWithinLiveWindowIsNotReassigned(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing lagging "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "lagging", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat lagging: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:lagging" {
		t.Fatalf("initial assign = %q err=%v, want w:lagging", got, err)
	}

	// Two missed ticks — inside the window by a minute — while a rival is fresh.
	killWorker(t, ctx, pool, "lagging", workerLiveWindow-time.Minute)
	if err := c.UpsertWorkerHeartbeat(ctx, "fresh", "203.0.113.2"); err != nil {
		t.Fatalf("heartbeat fresh: %v", err)
	}

	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:lagging" {
		t.Fatalf("resolve = %q err=%v, want w:lagging (still inside the live window)", got, err)
	}
	if worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID); !exists || worker != "lagging" {
		t.Fatalf("stored assignment = (%q, exists=%t), want (lagging, true)", worker, exists)
	}
}

// TestConcurrentAssignMailboxWorkerConvergesOnOneQueue: the first-send race. N
// senders resolve the same never-assigned mailbox at once against two live
// workers; each independently picks a least-loaded worker (they may disagree —
// the picks are unsynchronised reads), then races the upsert. The ON CONFLICT
// rule must make them all agree, because two IPs sending as one mailbox is the
// deliverability failure the pin exists to prevent.
//
// This is the case the liveness rewrite most easily breaks: an ON CONFLICT that
// unconditionally took EXCLUDED.worker_id would let every racer overwrite the
// last, and callers would return different queues for the same mailbox.
func TestConcurrentAssignMailboxWorkerConvergesOnOneQueue(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing race "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"race-a", "race-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.9"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)

	const senders = 8
	var wg sync.WaitGroup
	got := make([]string, senders)
	errs := make([]error, senders)
	for i := range senders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("sender %d: %v", i, err)
		}
	}
	for i, queueName := range got {
		if queueName != got[0] {
			t.Fatalf("sender %d resolved %q but sender 0 resolved %q — a mailbox split across two IPs", i, queueName, got[0])
		}
	}
	if got[0] != "w:race-a" && got[0] != "w:race-b" {
		t.Fatalf("converged queue = %q, want one of the live workers", got[0])
	}
	// The single stored row is the value every caller was handed.
	worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID)
	if !exists || workerQueuePrefix+worker != got[0] {
		t.Fatalf("stored worker %q (exists=%t) disagrees with the resolved queue %q", worker, exists, got[0])
	}
}

// TestConcurrentReassignmentOfStrandedMailboxConverges: the reassignment race.
// Two live workers both notice the same mailbox stranded on a dead worker and
// reassign it simultaneously. The upsert is atomic, so the first mover takes it
// and the second adopts that answer rather than stealing it back — otherwise a
// deploy that replaces N workers would flap every stranded mailbox between them.
func TestConcurrentReassignmentOfStrandedMailboxConverges(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing rerace "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "gone", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat gone: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil {
		t.Fatalf("initial assign: %v", err)
	}
	killWorker(t, ctx, pool, "gone", workerLiveWindow+time.Hour)
	for _, w := range []string{"repl-a", "repl-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.9"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	const senders = 8
	var wg sync.WaitGroup
	got := make([]string, senders)
	errs := make([]error, senders)
	for i := range senders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("sender %d: %v", i, err)
		}
	}
	for i, queueName := range got {
		if queueName != got[0] {
			t.Fatalf("sender %d resolved %q but sender 0 resolved %q", i, queueName, got[0])
		}
	}
	// Crucially it is NOT the dead worker's queue — converging on "w:gone" would be
	// unanimous agreement to keep dropping mail into a queue nobody consumes.
	if got[0] != "w:repl-a" && got[0] != "w:repl-b" {
		t.Fatalf("converged queue = %q, want a live replacement worker", got[0])
	}
	worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID)
	if !exists || workerQueuePrefix+worker != got[0] {
		t.Fatalf("stored worker %q (exists=%t) disagrees with the resolved queue %q", worker, exists, got[0])
	}
}

// TestAssignMailboxWorkerNoLiveWorkerKeepsStaleRowAndReturnsDefault: the whole
// fleet is down (or mid-restart) and a stale assignment exists. The assigner must
// return "" (shared default queue) and write NOTHING — in particular it must not
// "fix" the row by deleting it, since the send hot path is the wrong place to
// mutate routing state, and it must not return the dead worker's queue.
//
// The stale row surviving is deliberate: step 1's liveness join already ignores
// it, and reaping it is PurgeDeadWorkers' job.
func TestAssignMailboxWorkerNoLiveWorkerKeepsStaleRowAndReturnsDefault(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing fleetdown "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "only-node", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:only-node" {
		t.Fatalf("initial assign = %q err=%v, want w:only-node", got, err)
	}

	// The entire fleet goes silent, leaving the assignment stranded.
	killWorker(t, ctx, pool, "only-node", workerLiveWindow+time.Hour)

	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("assign with no live worker: %v", err)
	}
	if got != "" {
		t.Fatalf("no live worker must return the default queue \"\", got %q", got)
	}
	// The stale row is left exactly as it was — untouched, not deleted, not rewritten.
	worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID)
	if !exists || worker != "only-node" {
		t.Fatalf("stale row must survive untouched, got (%q, exists=%t)", worker, exists)
	}

	// Once a live worker returns, the very next resolve moves the mailbox onto it —
	// no manual intervention, and no dependence on the purge having run.
	if err := c.UpsertWorkerHeartbeat(ctx, "back-up", "203.0.113.2"); err != nil {
		t.Fatalf("heartbeat back-up: %v", err)
	}
	if again, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || again != "w:back-up" {
		t.Fatalf("assign after fleet recovery = %q err=%v, want w:back-up", again, err)
	}
}

// TestAssignMailboxWorkerCrossTenantOverStaleRow: the ON CONFLICT rewrite gave the
// upsert a DO UPDATE branch it never had, and DO UPDATE is not guarded by the
// INSERT ... SELECT's tenancy filter in any obvious way. Prove a foreign workspace
// still cannot take over an EXISTING row — the case a plain cross-tenant test
// (which starts with no row, so it never reaches the conflict) cannot reach.
func TestAssignMailboxWorkerCrossTenantOverStaleRow(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	owner, err := q.CreateWorkspace(ctx, "Routing owner "+uuid.NewString())
	if err != nil {
		t.Fatalf("owner workspace: %v", err)
	}
	foreign, err := q.CreateWorkspace(ctx, "Routing foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "owner-node", "203.0.113.1"); err != nil {
		t.Fatalf("heartbeat owner-node: %v", err)
	}
	mb := createRoutingMailbox(t, ctx, q, owner.ID)
	if _, err := c.AssignMailboxWorker(ctx, mb.String(), owner.ID.String()); err != nil {
		t.Fatalf("owner assign: %v", err)
	}
	// Strand the row so the DO UPDATE branch's "incumbent is dead, hand it over"
	// path is the one a foreign caller would trigger.
	killWorker(t, ctx, pool, "owner-node", workerLiveWindow+time.Hour)
	if err := c.UpsertWorkerHeartbeat(ctx, "attacker-node", "203.0.113.66"); err != nil {
		t.Fatalf("heartbeat attacker-node: %v", err)
	}

	if _, err := c.AssignMailboxWorker(ctx, mb.String(), foreign.ID.String()); !errors.Is(err, coreapi.ErrCrossTenant) {
		t.Fatalf("foreign reassign of a stranded row: err = %v, want ErrCrossTenant", err)
	}
	// The row still belongs to the owner's workspace and still names the old worker:
	// the foreign call changed nothing, and in particular did not repoint the
	// mailbox's egress at a worker chosen by another tenant.
	var rowWS uuid.UUID
	var rowWorker string
	if err := pool.QueryRow(ctx,
		"SELECT workspace_id, worker_id FROM mailbox_worker_assignments WHERE mailbox_id = $1", mb).
		Scan(&rowWS, &rowWorker); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if rowWS != owner.ID || rowWorker != "owner-node" {
		t.Fatalf("row after rejected cross-tenant reassign = (ws=%s, worker=%q), want (ws=%s, owner-node)", rowWS, rowWorker, owner.ID)
	}
	// The legitimate owner still gets the reassignment the stranded row needs.
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), owner.ID.String()); err != nil || got != "w:attacker-node" {
		t.Fatalf("owner reassign = %q err=%v, want w:attacker-node", got, err)
	}
}
