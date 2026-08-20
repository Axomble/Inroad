package inprocess

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// workerLiveWindow is how recently a worker must have heartbeated to be eligible
// for a new mailbox assignment. A worker heartbeats every workerHeartbeatInterval
// (5m in cmd/worker); a 15m window tolerates a couple of missed ticks before its
// mailboxes become eligible for a fresh assignment.
const workerLiveWindow = 15 * time.Minute

// workerQueuePrefix names a worker's dedicated queue: a mailbox assigned to
// worker W routes to / is consumed from "w:W". An empty worker id maps to the
// shared default queue ("").
const workerQueuePrefix = "w:"

// UpsertWorkerHeartbeat refreshes this worker's row in the global registry. See
// the coreapi.Client interface doc. `workers` is infra state, not tenant data,
// so there is no workspace pin here.
func (c client) UpsertWorkerHeartbeat(ctx context.Context, workerID, egressIP string) error {
	if workerID == "" {
		return fmt.Errorf("coreapi: worker id required for heartbeat")
	}
	if err := c.q.UpsertWorker(ctx, gen.UpsertWorkerParams{WorkerID: workerID, EgressIp: egressIP}); err != nil {
		return fmt.Errorf("coreapi: worker heartbeat: %w", err)
	}
	return nil
}

// AssignMailboxWorker resolves (and, on first sight, persists) the destination
// queue for a mailbox. See the coreapi.Client interface doc for the contract.
func (c client) AssignMailboxWorker(ctx context.Context, mailboxID, workspaceID string) (string, error) {
	mbID, err := uuid.Parse(mailboxID)
	if err != nil {
		return "", fmt.Errorf("coreapi: parse mailbox id: %w", err)
	}
	wsID, err := uuid.Parse(workspaceID)
	if err != nil {
		return "", fmt.Errorf("coreapi: parse workspace id: %w", err)
	}

	liveSince := pgtype.Timestamptz{Time: time.Now().Add(-workerLiveWindow), Valid: true}

	// 1. Idempotent: an existing assignment to a LIVE worker wins unchanged
	//    (workspace-pinned, so a foreign workspace_id matches zero rows and falls
	//    through to a fresh assignment scoped to ITS own workspace).
	//
	//    Liveness is checked here, not just when first assigning. An assignment
	//    whose worker stopped heartbeating routes to a queue no process consumes,
	//    and those tasks neither run nor fail nor alert — the mailbox goes quiet
	//    until someone deletes the row by hand. Every rolling deploy under a
	//    scheduler that changes instance identity produces exactly that state, so
	//    a stranded assignment is treated as no assignment and reassigned below.
	existing, err := c.q.GetLiveMailboxWorkerAssignment(ctx, gen.GetLiveMailboxWorkerAssignmentParams{
		MailboxID: mbID, WorkspaceID: wsID, LiveSince: liveSince,
	})
	switch {
	case err == nil:
		return queueForWorker(existing), nil
	case errors.Is(err, pgx.ErrNoRows):
		// No assignment, or one pinned to a worker that has gone silent — both
		// fall through to pick a live worker.
	default:
		return "", fmt.Errorf("coreapi: load assignment: %w", err)
	}

	// 2. Pick the least-loaded LIVE worker (heartbeat within the live window).
	workerID, err := c.q.PickLeastLoadedWorker(ctx, liveSince)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 3. No live worker (single-node dev, or the whole fleet mid-restart):
		//    shared default queue, no persist — so a real worker can claim this
		//    mailbox once it comes online. Any stale row from a dead worker is
		//    left in place rather than deleted here: this path runs on the send
		//    hot path, the row is already being ignored by step 1's liveness
		//    join, and step 4 overwrites it as soon as a live worker exists.
		//    Reaping it is the maintenance job's business, not the sender's.
		return "", nil
	case err != nil:
		return "", fmt.Errorf("coreapi: pick least-loaded worker: %w", err)
	}

	// 4. Persist the assignment. The INSERT ... SELECT writes a row ONLY when the
	//    mailbox belongs to wsID (self-enforcing tenancy, defense in depth on top of
	//    the SendJob resolver's own pin), so a mismatched pair inserts zero rows and
	//    RETURNING yields ErrNoRows here — distinct from step 3's no-live-worker
	//    ErrNoRows, which was on PickLeastLoadedWorker and returned "" WITHOUT
	//    reaching this insert. On conflict the row is kept for a LIVE incumbent
	//    (so both racers in a concurrent first-send agree) and handed to workerID
	//    when the incumbent has gone silent (so a stranded mailbox actually moves).
	//    liveSince makes that decision inside the statement, keeping it atomic
	//    against another worker reassigning the same mailbox.
	assigned, err := c.q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: mbID, WorkspaceID: wsID, WorkerID: workerID, LiveSince: liveSince,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Zero rows inserted: the mailbox does not belong to wsID. Fail closed —
		// never persist a foreign-workspace routing row.
		return "", coreapi.ErrCrossTenant
	case err != nil:
		return "", fmt.Errorf("coreapi: persist assignment: %w", err)
	}
	return queueForWorker(assigned), nil
}

// queueForWorker maps a worker_id to its queue name. An empty worker_id (never
// persisted, but belt-and-braces) maps to the shared default queue.
func queueForWorker(workerID string) string {
	if workerID == "" {
		return ""
	}
	return workerQueuePrefix + workerID
}
