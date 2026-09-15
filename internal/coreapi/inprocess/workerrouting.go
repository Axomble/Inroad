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
	"github.com/inroad/inroad/internal/platform/fleetdecision"
	"github.com/inroad/inroad/internal/platform/fleetscore"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// workerLiveWindow is how recently a worker must have heartbeated to be eligible
// for a new mailbox assignment. A worker heartbeats every workerHeartbeatInterval
// (5m in cmd/worker); a 15m window tolerates a couple of missed ticks before its
// mailboxes become eligible for a fresh assignment.
const workerLiveWindow = 15 * time.Minute

// providerSignalWindow is how far back placement reads a worker's provider
// verdicts. A worker flushes its accumulated counts every 5m (cmd/worker's
// workerSignalFlushInterval), so an hour is up to twelve windows — long enough
// that one quiet flush cannot make a blocked worker look healthy, short enough
// that a worker whose block cleared an hour ago is not still being punished for
// it. Deliberately far shorter than the table's 30-day retention: the placement
// question is "how is this egress IP being treated RIGHT NOW".
const providerSignalWindow = time.Hour

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
// queue for a mailbox, by SCORE across the live fleet (fleet F4). See the
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

	// 1. INCUMBENCY. An existing assignment to a LIVE worker wins unchanged, and
	//    nothing else is read or computed — this is the whole hot path, one
	//    indexed query, for every resolve after the first.
	//
	//    It is unconditional on purpose. IP trust accrues per (mailbox, IP) pair
	//    at the PROVIDER, which is what decides whether a sign-in from that
	//    address is challenged and how hard the address is throttled; moving a
	//    mailbox discards it. No packing preference the scorer can express is
	//    worth paying that, so "incumbency outranks packing" is control flow
	//    here rather than a very large weight in fleetscore — a weight nothing
	//    could outvote is not a weight.
	//
	//    Two conditions that used to sit here are gone with F5's tiers: the
	//    stored band no longer has to match the mailbox's CURRENT band, and a
	//    "mixed" incumbent is no longer re-evaluated. Both existed to MOVE a
	//    mailbox off a live worker, and moving one is rotation's decision, gated
	//    separately (a later PR), not something the send path makes in passing.
	//    A mailbox whose lane degrades therefore stays where it is: the band it
	//    moved between is derived from RECIPIENT-side evidence, and the
	//    recipient never observes the worker's egress IP (see fleetscore's
	//    package doc), so the move bought nothing and cost the IP pin.
	//
	//    Liveness is still checked on every resolve. An assignment whose worker
	//    stopped heartbeating routes to a queue no process consumes, and those
	//    tasks neither run nor fail nor alert — the mailbox goes quiet until
	//    someone deletes the row by hand. Every rolling deploy under a scheduler
	//    that changes instance identity produces exactly that state, so a
	//    stranded assignment is treated as no assignment and re-placed below.
	//
	//    Workspace-pinned, so a foreign workspace_id matches zero rows and falls
	//    through — where step 2 rejects it outright.
	incumbent, err := c.q.GetLiveMailboxWorkerAssignment(ctx, gen.GetLiveMailboxWorkerAssignmentParams{
		MailboxID: mbID, WorkspaceID: wsID, LiveSince: liveSince,
	})
	switch {
	case err == nil:
		// No decision-log entry: nothing was decided. The entry that put this
		// mailbox here is already in the log, and re-stating it on every warmup
		// tick would bury the decisions that did change something under
		// thousands of identical rows.
		return queueForWorker(incumbent), nil
	case errors.Is(err, pgx.ErrNoRows):
		c.reportStaleAssignment(ctx, mbID, wsID)
	default:
		return "", fmt.Errorf("coreapi: load assignment: %w", err)
	}

	// 2. The mailbox's own facts: which provider leg it runs (what it will cost
	//    a worker) and its warmup lane (its risk band). Zero rows means the
	//    mailbox does not belong to this workspace — fail closed here, before any
	//    placement work, rather than at the insert.
	facts, err := c.q.GetMailboxPlacementFacts(ctx, gen.GetMailboxPlacementFactsParams{ID: mbID, WorkspaceID: wsID})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", coreapi.ErrCrossTenant
	case err != nil:
		return "", fmt.Errorf("coreapi: load mailbox placement facts: %w", err)
	}
	// warmup.RiskBandForLane is the one source of truth for this mapping; an
	// empty lane (not a warmup participant at all) reads as healthy, so opting
	// out of warmup costs a mailbox nothing at placement time.
	band := warmup.RiskBandForLane(facts.Lane)

	liveCount, err := c.q.CountLiveWorkers(ctx, liveSince)
	if err != nil {
		return "", fmt.Errorf("coreapi: count live workers: %w", err)
	}

	p := placement{
		mailbox: mbID, workspace: wsID,
		provider: facts.Provider, band: band, liveSince: liveSince,
	}

	// 3. Self-host bypass: at most one live worker means there is no placement
	//    CHOICE to make, so nothing — not the band under F5, not the score or
	//    the health gate under F4 — may apply. Byte for byte the pre-F5,
	//    pre-F4 pick-and-persist for the single-worker topology, because every
	//    refinement above it can only ever amount to refusing to send from the
	//    one worker there is.
	if liveCount <= 1 {
		return c.placeOnSoleWorker(ctx, p)
	}

	return c.placeByScore(ctx, p)
}

// placement is one resolution in flight: which mailbox is being placed, the
// facts it will be scored on, and the live-worker cutoff every query in the
// attempt must share. It travels as one value because the alternative is
// threading the same five fields through three functions as parallel arguments,
// where a mailbox id and the workspace it is pinned to could drift apart at a
// call site.
type placement struct {
	mailbox   uuid.UUID
	workspace uuid.UUID
	// provider is the mailbox's transport leg, which decides both what it costs
	// a worker and which worker_provider_signals rows the health gate reads.
	provider string
	// band is the risk band the mailbox is being placed UNDER, recorded on the
	// assignment row and compared against every candidate's population.
	band string
	// liveSince is the heartbeat cutoff, computed ONCE per call so that the
	// candidate scan, the pick and the upsert all agree about which workers were
	// live — a cutoff recomputed per query could let a worker be live for the
	// scan and dead for the insert.
	liveSince pgtype.Timestamptz
}

// entry starts a decision-log entry for this placement, with the tenant pair
// already attached. Ids are strings at the fleetdecision seam like every other
// id crossing into coreapi.
func (p placement) entry(kind fleetdecision.Kind, workerID string, reason fleetdecision.Reason) fleetdecision.Entry {
	return fleetdecision.Entry{
		Kind:        kind,
		WorkerID:    workerID,
		MailboxID:   p.mailbox.String(),
		WorkspaceID: p.workspace.String(),
		Reason:      reason,
		// The actor is the ASSIGNMENT attempt, including when it refused: there
		// is no separate "refuse" automation, and naming one would imply a
		// component an operator could go and look at.
		TriggeredBy: fleetdecision.Auto(fleetdecision.KindAssign),
	}
}

// reportStaleAssignment distinguishes "this mailbox was never assigned" (the
// common first-send case, nothing worth reporting) from "its worker fell out of
// the live window" — two states GetLiveMailboxWorkerAssignment reports
// identically as pgx.ErrNoRows. Liveness expiry used to be completely silent, so
// an operator had no way to see a reassignment happening.
//
// Purely observability: its own failure must never block the send path this runs
// on, so it is logged and swallowed rather than propagated. It reports only that
// the incumbent went stale — whether a live replacement is actually available is
// a separate fact the caller has not established yet, and this message stays
// true either way.
func (c client) reportStaleAssignment(ctx context.Context, mbID, wsID uuid.UUID) {
	stale, err := c.q.MailboxWorkerAssignmentExists(ctx, gen.MailboxWorkerAssignmentExistsParams{
		MailboxID: mbID, WorkspaceID: wsID,
	})
	if err != nil {
		slog.WarnContext(ctx, "worker assignment staleness check failed", "mailbox_id", mbID, "err", err)
		return
	}
	if !stale {
		return
	}
	c.mtx.WorkerAssignmentStale()
	slog.WarnContext(ctx, "stale worker assignment: incumbent worker fell out of the live window",
		"mailbox_id", mbID, "workspace_id", wsID)
}

// placeOnSoleWorker is the self-host path: pick the one live worker and persist.
func (c client) placeOnSoleWorker(ctx context.Context, p placement) (string, error) {
	workerID, err := c.q.PickLeastLoadedWorker(ctx, p.liveSince)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No live worker at all (single-node dev, or the whole fleet
		// mid-restart): shared default queue, no persist — so a real worker can
		// claim this mailbox once it comes online. A stale row from a dead
		// worker is left in place rather than deleted here: this path runs on
		// the send hot path, the row is already ignored by step 1's liveness
		// join, and the next placement overwrites it as soon as a live worker
		// exists. Reaping it is the maintenance job's business, not the
		// sender's.
		//
		// No decision-log entry either, and deliberately: nothing was placed and
		// nothing persisted, so there is no decision to explain — and this
		// branch runs for EVERY mailbox on EVERY tick for as long as an outage
		// lasts, which is precisely the shape that turns a log into noise. The
		// closed kind vocabulary has no honest label for it: it is not an
		// assignment (no worker) and not a refusal (the caller still sends, via
		// the shared queue).
		return "", nil
	case err != nil:
		return "", fmt.Errorf("coreapi: pick least-loaded worker: %w", err)
	}

	assigned, err := c.persistAssignment(ctx, p, workerID)
	if err != nil {
		return "", err
	}
	reason := adoptedReason(workerID, assigned)
	if !reason.Valid() {
		reason = fleetdecision.Forced("the fleet has one live worker, so there was no placement choice to make")
	}
	c.recordPlacement(ctx, p.entry(fleetdecision.KindAssign, assigned, reason))
	return queueForWorker(assigned), nil
}

// placeByScore is fleet F4's placement proper: measure every live worker, score
// the eligible ones, take the best.
//
// There is no advisory lock here, and its absence is the design rather than an
// omission. F5's tier-2 promotion held one because ADOPTING an idle worker
// changed what "pure" meant for that worker for every future placement, and only
// one band could be allowed to win that decision — two mailboxes of different
// bands racing to promote the same idle worker would both succeed and mix it,
// which ON CONFLICT could not catch (the two INSERTs are for different
// mailbox_ids and never conflict with each other). Under F4 nothing about a
// worker becomes exclusive when a mailbox lands on it: a second placement onto
// the same worker is ordinary packing, and the loser of a race merely scored
// against a fleet state one mailbox out of date. The invariant that DID matter —
// two callers placing the SAME mailbox must converge on ONE worker — is
// unchanged and still enforced where it always was, inside
// InsertMailboxWorkerAssignment's ON CONFLICT.
func (c client) placeByScore(ctx context.Context, p placement) (string, error) {
	rows, err := c.q.ListPlacementCandidates(ctx, gen.ListPlacementCandidatesParams{
		LiveSince:    p.liveSince,
		WorkspaceID:  p.workspace,
		Band:         p.band,
		Provider:     p.provider,
		SignalsSince: pgtype.Timestamptz{Time: time.Now().Add(-providerSignalWindow), Valid: true},
	})
	if err != nil {
		return "", fmt.Errorf("coreapi: list placement candidates: %w", err)
	}

	ranked := fleetscore.Default().Rank(placementCandidates(rows), fleetscore.Incoming{Provider: p.provider})
	if len(ranked) == 0 {
		// Every live worker is refusing this provider's traffic. This is the ONLY
		// refusal placement can produce — the health gate is the only hard gate —
		// and it is genuinely "the fleet cannot serve this mailbox right now",
		// not "no worker matched a label".
		slog.WarnContext(ctx, "worker assignment refused: no eligible worker for this mailbox's provider",
			"mailbox_id", p.mailbox, "workspace_id", p.workspace, "provider", p.provider, "live_workers", len(rows))
		// No WorkerID on the entry: a refusal placed the mailbox nowhere, and
		// naming a worker it was refused FROM would read as the one it landed on.
		c.recordPlacement(ctx, p.entry(fleetdecision.KindRefused, "", fleetdecision.Forced(fmt.Sprintf(
			"every one of the %d live workers has recently been blocked or unreachable for provider %q and none has completed an operation since; add fleet capacity or wait for the block to clear",
			len(rows), p.provider))))
		return "", coreapi.ErrNoEligibleWorker
	}

	assigned, err := c.persistAssignment(ctx, p, ranked[0].WorkerID)
	if err != nil {
		return "", err
	}
	c.recordPlacement(ctx, p.entry(fleetdecision.KindAssign, assigned, placementReason(ranked, len(rows), assigned)))
	return queueForWorker(assigned), nil
}

// placementCandidates maps the query's rows onto the scorer's input. The counts
// are int64 in Postgres and int in the policy; a count that overflowed an int
// would mean a single worker carrying more mailboxes than this process can
// address, so the conversion is safe by construction rather than by check.
func placementCandidates(rows []gen.ListPlacementCandidatesRow) []fleetscore.Candidate {
	out := make([]fleetscore.Candidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, fleetscore.Candidate{
			WorkerID:               r.WorkerID,
			SMTPMailboxes:          int(r.SmtpMailboxes),
			GmailMailboxes:         int(r.GmailMailboxes),
			M365Mailboxes:          int(r.M365Mailboxes),
			SameWorkspaceMailboxes: int(r.SameWorkspaceMailboxes),
			OtherBandMailboxes:     int(r.OtherBandMailboxes),
			ProviderOKEvents:       r.OkEvents,
			ProviderBlockEvents:    r.BlockEvents,
			ProviderThrottleEvents: r.ThrottleEvents,
		})
	}
	return out
}

// placementReason renders what ACTUALLY happened, which is not always what the
// scorer chose: a concurrent caller may have placed this mailbox first, and the
// upsert then keeps that live incumbent. Logging "chose X (score 4.90) over Y"
// when the row says Z would publish a comparison that decided nothing.
//
// A single eligible candidate is reported as uncontested rather than as a
// comparison against an invented runner-up — fleetdecision enforces that by
// construction, and this function's job is to hand it the honest inputs.
func placementReason(ranked []fleetscore.Ranked, considered int, assigned string) fleetdecision.Reason {
	if len(ranked) == 0 {
		// Unreachable from placeByScore, which refuses before persisting.
		// Returning a valid Reason anyway keeps a future caller from writing an
		// entry that Validate rejects.
		return fleetdecision.Forced("no candidate was scored")
	}
	if r := adoptedReason(ranked[0].WorkerID, assigned); r.Valid() {
		return r
	}
	winner := fleetdecision.Candidate{WorkerID: ranked[0].WorkerID, Score: ranked[0].Score}
	if len(ranked) == 1 {
		return fleetdecision.ChoseUncontested(winner, considered)
	}
	return fleetdecision.Chose(winner, fleetdecision.Candidate{WorkerID: ranked[1].WorkerID, Score: ranked[1].Score}, considered)
}

// adoptedReason explains a placement that landed somewhere other than where the
// caller aimed: another caller got there first and the upsert kept its live
// incumbent. It returns the zero Reason when the placement went where it was
// aimed, so callers can treat "invalid" as "nothing unusual happened" and fall
// through to their own reason.
func adoptedReason(picked, assigned string) fleetdecision.Reason {
	if picked == assigned {
		return fleetdecision.Reason{}
	}
	return fleetdecision.Forced(fmt.Sprintf(
		"a concurrent caller had already placed this mailbox on %s; that live assignment was adopted rather than re-pointing the mailbox at %s",
		assigned, picked))
}

// recordPlacement appends one decision-log entry for a placement that actually
// happened — a persisted assignment, or a refusal. Best effort by design: a
// decision that could not be logged is degraded observability, and failing the
// send path over a missing log line would turn that into a mailbox that stops
// sending.
func (c client) recordPlacement(ctx context.Context, e fleetdecision.Entry) {
	if err := c.RecordFleetDecision(ctx, e); err != nil {
		slog.WarnContext(ctx, "fleet decision not recorded",
			"kind", e.Kind, "mailbox_id", e.MailboxID, "err", err)
	}
}

// persistAssignment upserts the placement and returns the worker that actually
// owns the mailbox afterwards — which is the CALLER's pick unless a concurrent
// caller placed it first, in which case it is that live incumbent. Callers must
// use the returned id rather than the one they passed, both for the queue they
// hand back and for anything they log.
//
// The INSERT ... SELECT writes a row ONLY when the mailbox belongs to wsID
// (self-enforcing tenancy, defense in depth on top of the resolver's own pin),
// so a mismatched pair inserts zero rows and RETURNING yields ErrNoRows here. On
// conflict the row is kept for a LIVE incumbent and handed to workerID
// otherwise; liveSince makes that decision inside the statement, keeping it
// atomic against another caller re-placing the same mailbox.
func (c client) persistAssignment(ctx context.Context, p placement, workerID string) (string, error) {
	assigned, err := c.q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: p.mailbox, WorkspaceID: p.workspace, WorkerID: workerID, Band: p.band, LiveSince: p.liveSince,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Zero rows inserted: the mailbox does not belong to wsID. Fail closed —
		// never persist a foreign-workspace routing row.
		return "", coreapi.ErrCrossTenant
	case err != nil:
		return "", fmt.Errorf("coreapi: persist assignment: %w", err)
	}
	return assigned, nil
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
