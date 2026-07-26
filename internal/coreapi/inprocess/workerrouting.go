package inprocess

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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

	// 1. Idempotent: an existing assignment wins unchanged (workspace-pinned, so
	//    a foreign workspace_id matches zero rows and falls through to a fresh
	//    assignment scoped to ITS own workspace).
	existing, err := c.q.GetMailboxWorkerAssignment(ctx, gen.GetMailboxWorkerAssignmentParams{
		MailboxID: mbID, WorkspaceID: wsID,
	})
	switch {
	case err == nil:
		return queueForWorker(existing), nil
	case errors.Is(err, pgx.ErrNoRows):
		// no assignment yet — fall through to a first assignment
	default:
		return "", fmt.Errorf("coreapi: load assignment: %w", err)
	}

	// 2. Pick the least-loaded LIVE worker (heartbeat within the live window).
	liveSince := pgtype.Timestamptz{Time: time.Now().Add(-workerLiveWindow), Valid: true}
	workerID, err := c.q.PickLeastLoadedWorker(ctx, liveSince)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 3. No live worker (single-node dev): shared default queue, no persist —
		//    so a real worker can claim this mailbox once it comes online.
		return "", nil
	case err != nil:
		return "", fmt.Errorf("coreapi: pick least-loaded worker: %w", err)
	}

	// 4. Persist the assignment. ON CONFLICT keeps the row that won a concurrent
	//    first-send race and returns ITS worker_id, so both racers agree.
	assigned, err := c.q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: mbID, WorkspaceID: wsID, WorkerID: workerID,
	})
	if err != nil {
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
