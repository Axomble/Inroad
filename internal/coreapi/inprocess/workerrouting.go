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
func (c client) UpsertWorkerHeartbeat(ctx context.Context, workerID, egressIP, idFamily string) error {
	if workerID == "" {
		return fmt.Errorf("coreapi: worker id required for heartbeat")
	}
	if err := c.q.UpsertWorker(ctx, gen.UpsertWorkerParams{WorkerID: workerID, EgressIp: egressIP, IDFamily: idFamily}); err != nil {
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

	// How many workers are live at all decides whether segregation applies —
	// needed here (before the idempotent check below) rather than only at pick
	// time, because it also decides whether a MIXED incumbent worker is worth
	// leaving. Self-host (and any fleet mid-restart down to one node) has no
	// SECOND worker to segregate onto — there is no choice to make, so
	// refusing (or even re-evaluating) would serve no purpose, only cost an
	// extra round trip on every call. liveCount==0 also takes the self-host
	// branch below: PickLeastLoadedWorker's own no-live-worker path is
	// unaffected by band logic either way.
	liveCount, err := c.q.CountLiveWorkers(ctx, liveSince)
	if err != nil {
		return "", fmt.Errorf("coreapi: count live workers: %w", err)
	}

	// 1. Idempotent: an existing assignment to a LIVE worker in the SAME band
	//    wins unchanged (workspace-pinned, so a foreign workspace_id matches zero
	//    rows and falls through to a fresh assignment scoped to ITS own
	//    workspace) — PROVIDED that worker is not currently mixed, or there is
	//    no better place for it anyway (liveCount <= 1). A mailbox parked on an
	//    already-mixed worker (fix-round-1, Important 2's tier-3 fallback) must
	//    keep re-evaluating on every call even though ITS OWN band never
	//    changed, so it can migrate off lazily once a pure or idle worker opens
	//    up — otherwise "the fleet converges toward purity over time" would be
	//    false: nothing would ever move a mailbox that landed on a mixed worker
	//    once, and it would sit there forever.
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
		if existing.Band == band && (liveCount <= 1 || !existing.WorkerMixed) {
			return queueForWorker(existing.WorkerID), nil
		}
		// Band mismatch, or a mixed incumbent worth leaving: the row is stale
		// evidence of a decision that no longer holds (or no longer holds the
		// best answer). Fall through exactly like a dead-worker row does —
		// pick again.
	case errors.Is(err, pgx.ErrNoRows):
		// No assignment, or one pinned to a worker that has gone silent — both
		// fall through to pick a live worker. Liveness expiry used to be
		// completely silent here: a dead worker's assignment and "never
		// assigned at all" produced the identical ErrNoRows, so an operator
		// had no way to see the reassignment happening. Distinguish the two
		// with the cheap existence check below, purely for observability —
		// its own failure must never block the send path this runs on, so it
		// is logged and swallowed, not propagated.
		if stale, existsErr := c.q.MailboxWorkerAssignmentExists(ctx, gen.MailboxWorkerAssignmentExistsParams{
			MailboxID: mbID, WorkspaceID: wsID,
		}); existsErr != nil {
			slog.WarnContext(ctx, "worker assignment staleness check failed", "mailbox_id", mailboxID, "err", existsErr)
		} else if stale {
			// Reported here, before step 2 even runs, deliberately: whether a
			// live replacement is actually available yet is a SEPARATE fact
			// (step 3 below still falls back to "" with nothing live), and
			// this message must stay true either way — it only claims the
			// incumbent went stale, not that a new one was found.
			c.mtx.WorkerAssignmentStale()
			slog.WarnContext(ctx, "stale worker assignment: incumbent worker fell out of the live window",
				"mailbox_id", mailboxID, "workspace_id", workspaceID)
		}
	default:
		return "", fmt.Errorf("coreapi: load assignment: %w", err)
	}

	// 2. Self-host bypass: at most one live worker means there is no placement
	//    CHOICE to make, so segregation does not apply at all — the exact
	//    pre-F5 pick-and-persist, byte for byte, for the single-worker
	//    topology (a single-worker fleet must never refuse to send because of
	//    a band it has no second IP to isolate onto).
	if liveCount <= 1 {
		workerID, err := c.q.PickLeastLoadedWorker(ctx, liveSince)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// No live worker at all (single-node dev, or the whole fleet
			// mid-restart): shared default queue, no persist — so a real
			// worker can claim this mailbox once it comes online. A stale
			// row from a dead worker is left in place rather than deleted
			// here: this path runs on the send hot path, the row is already
			// ignored by step 1's liveness join, and persistAssignment
			// overwrites it as soon as a live worker exists. Reaping it is
			// the maintenance job's business, not the sender's.
			return "", nil
		case err != nil:
			return "", fmt.Errorf("coreapi: pick least-loaded worker: %w", err)
		}
		return c.persistAssignment(ctx, mbID, wsID, workerID, band, liveSince)
	}

	// 3. Segregated placement across the multi-worker fleet (requirements 2-4).
	queueName, err := c.assignBandedWorker(ctx, mbID, wsID, band, liveSince)
	if errors.Is(err, coreapi.ErrNoBandCapacity) {
		slog.WarnContext(ctx, "worker assignment refused: no capacity in mailbox's risk band",
			"mailbox_id", mailboxID, "workspace_id", workspaceID, "band", band, "live_workers", liveCount)
		return "", coreapi.ErrNoBandCapacity
	}
	return queueName, err
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

// assignBandedWorker resolves AND persists a placement across three tiers,
// returning the final queue name. It owns its own persist step for every
// tier (rather than returning a worker_id for a shared generic insert)
// because tier 2's pick-then-persist must be atomic under claimIdleWorker's
// advisory lock (fix-round-1, Important 1) — splitting persistence out to a
// step that runs AFTER this function returns would reopen the exact race the
// lock exists to close, by letting the insert happen outside the locked
// transaction.
//
// Tier 1 prefers a worker that is ALREADY PURELY this mailbox's band — never
// a worker that merely carries SOME of this band (fix-round-1, Important 2:
// see PickPureWorkerForBand's doc for why that distinction is the whole
// fix). Tier 2 promotes a genuinely idle worker. Tier 3, last resort, adds to
// an already-mixed worker rather than refusing outright — refusing when the
// ENTIRE fleet is already mixed (the realistic state of a real fleet on this
// feature's first deploy) would stop sending altogether, which is worse than
// an imperfectly segregated placement that keeps draining toward purity
// (requirement 4 / step 1's WorkerMixed re-check migrates it off lazily).
// Only when none of the three has room does it refuse with
// ErrNoBandCapacity — never falling back to a worker PURELY in the OTHER
// band, which is the one thing this function exists to prevent.
func (c client) assignBandedWorker(ctx context.Context, mbID, wsID uuid.UUID, band string, liveSince pgtype.Timestamptz) (string, error) {
	workerID, err := c.q.PickPureWorkerForBand(ctx, gen.PickPureWorkerForBandParams{LiveSince: liveSince, Band: band})
	switch {
	case err == nil:
		return c.persistAssignment(ctx, mbID, wsID, workerID, band, liveSince)
	case errors.Is(err, pgx.ErrNoRows):
		// No worker is purely this band yet — try to promote an idle one.
	default:
		return "", fmt.Errorf("coreapi: pick pure worker for band: %w", err)
	}

	queueName, err := c.claimIdleWorker(ctx, mbID, wsID, band, liveSince)
	switch {
	case err == nil:
		return queueName, nil
	case errors.Is(err, pgx.ErrNoRows):
		// No idle worker either — the last resort: an already-mixed one.
	default:
		return "", err
	}

	workerID, err = c.q.PickMixedWorker(ctx, liveSince)
	switch {
	case err == nil:
		slog.WarnContext(ctx, "worker assignment landed on an already-mixed worker; it migrates off once a pure or idle worker is available",
			"mailbox_id", mbID, "workspace_id", wsID, "band", band, "worker_id", workerID)
		return c.persistAssignment(ctx, mbID, wsID, workerID, band, liveSince)
	case errors.Is(err, pgx.ErrNoRows):
		return "", coreapi.ErrNoBandCapacity
	default:
		return "", fmt.Errorf("coreapi: pick mixed worker: %w", err)
	}
}

// claimIdleWorker is tier 2: atomically claim a genuinely idle live worker
// into `band` inside one transaction, closing the TOCTOU race two DIFFERENT
// mailboxes of DIFFERENT bands would otherwise hit racing to promote the SAME
// idle worker (fix-round-1, Important 1 — ON CONFLICT on
// mailbox_worker_assignments cannot catch this, because the two racing
// INSERTs are for different mailbox_ids and so never conflict with each
// other; proven by TestAssignMailboxWorkerIdlePromotionRaceNeverMixesAWorker,
// which fails when the pick and the insert run as two separate,
// non-transactional statements).
//
// A pgx.ErrNoRows return means "no idle worker right now" — from either the
// pick itself, or (belt-and-braces) a cross-tenant insert would be mapped to
// coreapi.ErrCrossTenant instead, which is NOT pgx.ErrNoRows, so the caller's
// errors.Is check correctly treats it as a hard error rather than silently
// falling through to tier 3.
func (c client) claimIdleWorker(ctx context.Context, mbID, wsID uuid.UUID, band string, liveSince pgtype.Timestamptz) (string, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("coreapi: begin idle-promotion tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := c.q.WithTx(tx)

	// Serializes ONLY this promotion decision across concurrent callers — see
	// LockWorkerPromotion's doc for why tiers 1 and 3 need no lock at all.
	// Auto-released at commit/rollback (the transaction-scoped form), never
	// stranded on a pooled connection reused for something else.
	if err := qtx.LockWorkerPromotion(ctx); err != nil {
		return "", fmt.Errorf("coreapi: lock worker promotion: %w", err)
	}

	workerID, err := qtx.PickIdleLiveWorker(ctx, liveSince)
	if err != nil {
		return "", err
	}

	assigned, err := qtx.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: mbID, WorkspaceID: wsID, WorkerID: workerID, Band: band, LiveSince: liveSince,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", coreapi.ErrCrossTenant
	case err != nil:
		return "", fmt.Errorf("coreapi: persist idle-promotion assignment: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("coreapi: commit idle-promotion: %w", err)
	}
	return queueForWorker(assigned), nil
}

// persistAssignment is the non-transactional upsert shared by the self-host
// path, tier 1 and tier 3 — none of which are contested by a concurrent
// DIFFERENT mailbox (distinct mailbox_ids always insert distinct rows), so
// none of them need claimIdleWorker's transaction or lock. The INSERT ...
// SELECT writes a row ONLY when the mailbox belongs to wsID (self-enforcing
// tenancy, defense in depth on top of the SendJob resolver's own pin), so a
// mismatched pair inserts zero rows and RETURNING yields ErrNoRows here. On
// conflict the row is kept for a LIVE, SAME-BAND incumbent (so both racers in
// a concurrent first-send for the SAME mailbox agree) and handed to workerID
// otherwise — the incumbent has gone silent, OR its band no longer matches
// (the migration path requirement 4 needs). liveSince makes that decision
// inside the statement, keeping it atomic against another caller reassigning
// the same mailbox.
func (c client) persistAssignment(ctx context.Context, mbID, wsID uuid.UUID, workerID, band string, liveSince pgtype.Timestamptz) (string, error) {
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
