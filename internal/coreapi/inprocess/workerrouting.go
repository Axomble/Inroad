package inprocess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// workerLiveWindow is how recently a worker must have heartbeated to be eligible
// for a new mailbox assignment. A worker heartbeats every workerHeartbeatInterval
// (5m in cmd/worker); a 15m window tolerates a couple of missed ticks before its
// mailboxes become eligible for a fresh assignment.
const workerLiveWindow = 15 * time.Minute

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
// queue for a mailbox, enforcing risk-band segregation (fleet F5). See the
// coreapi.Client interface doc for the full contract.
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

	// Computed FIRST, before the idempotent read below, because that read has to
	// compare the row's STORED band against the mailbox's CURRENT one — a
	// mismatch (the lane moved since the row was written) must be treated like a
	// dead worker and fall through to reassignment, which is how requirement 4
	// ("a band change must move the mailbox") is satisfied: every call recomputes
	// and compares, so the very next call after a lane change reassigns.
	band, err := c.mailboxRiskBand(ctx, mbID, wsID)
	if err != nil {
		return "", err
	}

	// 1. Idempotent: an existing assignment to a LIVE worker in the SAME band
	//    wins unchanged (workspace-pinned, so a foreign workspace_id matches zero
	//    rows and falls through to a fresh assignment scoped to ITS own
	//    workspace).
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
		if existing.Band == band {
			return queueForWorker(existing.WorkerID), nil
		}
		// Band mismatch: the row is stale evidence of a decision that no longer
		// holds. Fall through exactly like a dead-worker row does — pick again.
	case errors.Is(err, pgx.ErrNoRows):
		// No assignment, or one pinned to a worker that has gone silent — both
		// fall through to pick a live worker.
	default:
		return "", fmt.Errorf("coreapi: load assignment: %w", err)
	}

	// 2. How many workers are live at all decides whether segregation applies.
	//    Self-host (and any fleet mid-restart down to one node) has no SECOND
	//    worker to segregate onto — there is no choice to make, so refusing would
	//    only mean refusing to send at all, which this feature must never do.
	//    liveCount==0 also takes this branch: PickLeastLoadedWorker's own
	//    no-live-worker path below is unaffected by band logic either way.
	liveCount, err := c.q.CountLiveWorkers(ctx, liveSince)
	if err != nil {
		return "", fmt.Errorf("coreapi: count live workers: %w", err)
	}

	// 3. Pick a worker. At most one live worker: the unsegregated legacy pick
	//    (self-host bypass, step 2's comment). Otherwise: band-matched or
	//    idle-promoted only — never off-band.
	var workerID string
	if liveCount <= 1 {
		workerID, err = c.q.PickLeastLoadedWorker(ctx, liveSince)
	} else {
		workerID, err = c.pickBandedWorker(ctx, band, liveSince)
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No live worker at all (single-node dev, or the whole fleet
		// mid-restart): shared default queue, no persist — so a real worker can
		// claim this mailbox once it comes online. Any stale row from a dead
		// worker is left in place rather than deleted here: this path runs on
		// the send hot path, the row is already being ignored by step 1's
		// liveness join, and step 4 overwrites it as soon as a live worker
		// exists. Reaping it is the maintenance job's business, not the
		// sender's.
		return "", nil
	case errors.Is(err, coreapi.ErrNoBandCapacity):
		slog.Warn("worker assignment refused: no capacity in mailbox's risk band",
			"mailbox_id", mailboxID, "workspace_id", workspaceID, "band", band, "live_workers", liveCount)
		return "", coreapi.ErrNoBandCapacity
	case err != nil:
		return "", fmt.Errorf("coreapi: pick worker: %w", err)
	}

	// 4. Persist the assignment. The INSERT ... SELECT writes a row ONLY when the
	//    mailbox belongs to wsID (self-enforcing tenancy, defense in depth on top of
	//    the SendJob resolver's own pin), so a mismatched pair inserts zero rows and
	//    RETURNING yields ErrNoRows here — distinct from step 3's no-live-worker
	//    ErrNoRows, which was on the pick and returned "" WITHOUT reaching this
	//    insert. On conflict the row is kept for a LIVE, SAME-BAND incumbent (so
	//    both racers in a concurrent first-send agree) and handed to workerID
	//    otherwise — the incumbent has gone silent, OR its band no longer matches
	//    (the migration path requirement 4 needs). liveSince makes that decision
	//    inside the statement, keeping it atomic against another worker
	//    reassigning the same mailbox.
	assigned, err := c.q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: mbID, WorkspaceID: wsID, WorkerID: workerID, Band: band, LiveSince: liveSince,
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

// mailboxRiskBand derives the mailbox's CURRENT risk band from its warmup
// lane — the one source of truth (warmup.RiskBandForLane), never a parallel
// health concept computed here. A mailbox with no warmup_participants row is
// not enrolled in warmup at all and is treated as healthy (RiskBandForLane's
// "" case): opting out of warmup cannot cost a mailbox placement alongside the
// healthy pool any more than it costs it new campaign leads.
func (c client) mailboxRiskBand(ctx context.Context, mbID, wsID uuid.UUID) (string, error) {
	p, err := c.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: mbID, WorkspaceID: wsID})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return warmup.RiskBandForLane(""), nil
	case err != nil:
		return "", fmt.Errorf("coreapi: load warmup participant for risk band: %w", err)
	}
	return warmup.RiskBandForLane(p.Lane), nil
}

// pickBandedWorker is step 2's multi-worker branch: strict segregation
// (requirement 2), with the idle-worker promotion path (requirement 3).
//
// It prefers a live worker that ALREADY carries this band; failing that, a
// live worker carrying NOTHING may be promoted into it. Only when NEITHER
// exists does it refuse with ErrNoBandCapacity — never falling back to a
// worker in the OTHER band, which is the one thing this function exists to
// prevent.
func (c client) pickBandedWorker(ctx context.Context, band string, liveSince pgtype.Timestamptz) (string, error) {
	workerID, err := c.q.PickLeastLoadedWorkerForBand(ctx, gen.PickLeastLoadedWorkerForBandParams{
		LiveSince: liveSince, Band: band,
	})
	switch {
	case err == nil:
		return workerID, nil
	case errors.Is(err, pgx.ErrNoRows):
		// No worker already in this band — try to promote an idle one.
	default:
		return "", fmt.Errorf("coreapi: pick least-loaded worker for band: %w", err)
	}

	workerID, err = c.q.PickIdleLiveWorker(ctx, liveSince)
	switch {
	case err == nil:
		return workerID, nil
	case errors.Is(err, pgx.ErrNoRows):
		return "", coreapi.ErrNoBandCapacity
	default:
		return "", fmt.Errorf("coreapi: pick idle worker: %w", err)
	}
}

// queueForWorker maps a worker_id to its dedicated affinity queue
// (queue.WorkerQueue). An empty worker_id (never persisted, but
// belt-and-braces) is returned unchanged as "" — the sentinel AssignMailboxWorker
// also uses for "no live worker was found" — rather than this package
// resolving it to a queue itself. What an unassigned mailbox falls back to is
// the CALLER's decision (queue.Client.EnqueueWarmupTickAt currently falls
// back to the send role queue): this package has no opinion on role queues
// and must not bake in what "" happens to mean downstream today.
func queueForWorker(workerID string) string {
	if workerID == "" {
		return ""
	}
	return queue.WorkerQueue(workerID)
}
