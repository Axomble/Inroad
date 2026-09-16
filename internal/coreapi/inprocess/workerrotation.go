package inprocess

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/fleetdecision"
	"github.com/inroad/inroad/internal/platform/fleetrotate"
	"github.com/inroad/inroad/internal/platform/fleetscore"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// rotationScanLimit is how many assignments ONE rotation tick looks at.
//
// It is a bound on work, not on moves — fleetrotate.Policy.MaxMoves is the bound
// on moves, and it is smaller. The gap is what lets a tick examine mailboxes it
// then declines to move, which is the normal outcome: the scan's SQL prefilter is
// a deliberate superset of what the policy accepts.
//
// 40 costs at most a few dozen candidate measurements per tick (the destination
// lookup is memoised per (workspace, band, provider) and re-read after every
// applied move — see rotationFleets), against a tick that fires every five
// minutes. The scan is ordered urgent-first, so a fleet with more than 40
// qualifying assignments drains the broken ones first; which of the merely
// long-resident ones a tick sees is decided by rotationScanCursor below.
const rotationScanLimit = 40

// rotationScanSweep is how long the scan cursor takes to travel once around the
// mailbox-id space, and with it how long a settled assignment may wait before a
// tick looks at it again.
//
// WHAT IT IS FOR. rotationScanLimit reads a fixed number of rows; the SETTLED
// population it reads them from is unbounded (every assignment older than the
// residency floor qualifies, which after twelve hours is most of the fleet).
// Ordered by residency alone the same rows came back on every tick forever — a
// settled assignment is not changed by being scanned, so a window full of rows
// the policy declines never drains and everything behind it was starved
// permanently. The cursor makes the window an unbiased moving sample instead of
// a frozen prefix. See the ORDER BY comment on ListRotationCandidates.
//
// 24 HOURS, and the number is chosen against two other numbers:
//
//   - The pass ticks every 5 minutes (queue.RegisterFleetRotate), so the cursor
//     advances a 288th of the id space per tick. While a 288th of the fleet's
//     settled assignments is under rotationScanLimit — about 11,500 of them —
//     consecutive ticks' windows OVERLAP and the sweep misses nothing. Past that
//     size a sweep samples rather than enumerates, which is still the property
//     that matters: no row's wait depends on where it sits.
//   - fleetrotate's residency floor is 12 hours, so a mailbox the cursor moved
//     is comfortably past the floor by the time the cursor comes round to it
//     again. The sweep never arrives to find a mailbox it must decline purely
//     because it was here last time.
//
// This does NOT raise how much the fleet may move. That is fleetrotate.Policy's
// MaxMoves and MaxMovesPerDestination, both untouched, and every move the wider
// reach enables still has to clear the residency floor and the score margin.
const rotationScanSweep = 24 * time.Hour

// rotationScanCursorStep is how far one nanosecond of a sweep advances the
// cursor through the top 64 bits of the id space. Integer arithmetic, and
// deliberately the whole range divided by the whole sweep: the largest elapsed
// value the sweep can produce times this cannot overflow uint64, which a
// float-free mapping has to be able to state.
const rotationScanCursorStep = math.MaxUint64 / uint64(rotationScanSweep)

// rotationScanCursor is where the scan's ring starts for a tick happening at
// `now`: the point in the mailbox-id space the tick reads forward from.
//
// Derived from the WALL CLOCK and nothing else, which is what keeps this
// stateless. There is no cursor column, no counter, and nothing to reconcile
// when a tick is missed or a control-plane host restarts — two processes ticking
// at the same instant compute the same cursor, and a fleet that skipped an hour
// of ticks simply resumes where the clock says it should be. (Truncate works on
// absolute time since the zero instant, so the phase does not depend on the
// host's timezone.)
//
// The result is a bound for a comparison and never a stored id: only the top 64
// bits vary, and it is not a valid v4 uuid. That is fine, and so is comparing it
// against ones that are. mailboxes.id has no client-supplied path — every row
// takes gen_random_uuid() (migration 000002) — and v4's fixed version and
// variant nibbles sit in bytes 6 and 8, below the 48 fully random leading bits
// that decide the ordering. A cursor uniform over the space therefore samples
// the rows uniformly, which is the whole property: no assignment's wait depends
// on where in the fleet it happens to sit.
func rotationScanCursor(now time.Time) uuid.UUID {
	elapsed := uint64(now.Sub(now.Truncate(rotationScanSweep)))
	var cursor uuid.UUID
	binary.BigEndian.PutUint64(cursor[:8], elapsed*rotationScanCursorStep)
	return cursor
}

// rotationStore is the slice of the generated query set the rotation tick uses.
// Consumer-defined and four methods wide, so the tick's control flow — the
// priority order, the move budget, the destination cache and what happens when a
// guarded write matches nothing — is unit-testable against a fake with no
// Postgres anywhere (workerrotation_test.go). *gen.Queries satisfies it.
type rotationStore interface {
	CountLiveWorkers(ctx context.Context, liveSince pgtype.Timestamptz) (int64, error)
	ListRotationCandidates(ctx context.Context, arg gen.ListRotationCandidatesParams) ([]gen.ListRotationCandidatesRow, error)
	ListPlacementCandidates(ctx context.Context, arg gen.ListPlacementCandidatesParams) ([]gen.ListPlacementCandidatesRow, error)
	RotateMailboxWorkerAssignment(ctx context.Context, arg gen.RotateMailboxWorkerAssignmentParams) (string, error)
}

// rotationTick is everything ONE pass needs that is not the context: its two
// seams, its instrumentation, its policy and where in the fleet it starts
// reading. A struct rather than five more positional parameters, so a call site
// says which is which and so a later input (or a later collaborator) does not
// reopen every existing one.
type rotationTick struct {
	// store is the four queries the pass issues. Consumer-defined — see
	// rotationStore.
	store rotationStore
	// recorder appends the decision log. Its failures are degraded
	// observability, never a failed tick (see applyRotation).
	recorder coreapi.FleetDecisionRecorder
	// mtx counts the moves that actually landed, by tier. NIL IS VALID and is
	// what every unit test and cmd/seed gets: *metrics.Metrics is
	// nil-receiver-safe throughout, so there is no branch here for it.
	mtx *metrics.Metrics
	// policy holds the brakes: the tiers, the residency floor, the score margin
	// and the per-tick caps.
	policy fleetrotate.Policy
	// scanCursor is where this tick reads the settled tail of the fleet from —
	// see rotationScanCursor, which is what production passes. The zero value is
	// meaningful rather than missing: it is the ring's origin, so a caller that
	// leaves it out gets the lowest ids first on every tick, which is exactly the
	// frozen window the cursor exists to unfreeze.
	scanCursor uuid.UUID
}

// RotateMailboxWorkers runs one rotation tick. See the coreapi.FleetRotator
// interface doc for the contract.
func (c client) RotateMailboxWorkers(ctx context.Context) (int64, error) {
	return rotateFleet(ctx, rotationTick{
		store:      c.q,
		recorder:   c,
		mtx:        c.mtx,
		policy:     fleetrotate.Default(),
		scanCursor: rotationScanCursor(time.Now()),
	})
}

// rotateFleet is the tick proper, over interfaces rather than over the client,
// so the orchestration can be exercised without a database and so the decision
// log is written through coreapi.FleetDecisionRecorder — the seam that exists
// precisely to stop a call site hand-building prose that claims a comparison it
// never made.
//
// The order of business is: refuse if there is nowhere to rotate to, scan,
// classify and prioritise, then consider one mailbox at a time until the budget
// is spent.
func rotateFleet(ctx context.Context, tick rotationTick) (int64, error) {
	q, p := tick.store, tick.policy
	now := time.Now()
	liveSince := pgtype.Timestamptz{Time: now.Add(-workerLiveWindow), Valid: true}

	// SELF-HOST FIRST, and before any scan. With at most one live worker there
	// is nowhere to rotate TO, so every mailbox in the fleet would be considered
	// and declined on every tick — work, log noise and a decision-log entry per
	// mailbox per tick, all to conclude the one thing that was knowable from a
	// single count. One query, no rows read, no entry written.
	liveCount, err := q.CountLiveWorkers(ctx, liveSince)
	if err != nil {
		return 0, fmt.Errorf("coreapi: count live workers: %w", err)
	}
	if liveCount <= 1 {
		slog.DebugContext(ctx, "fleet rotation skipped: no second worker to rotate to", "live_workers", liveCount)
		return 0, nil
	}

	rows, err := q.ListRotationCandidates(ctx, gen.ListRotationCandidatesParams{
		LiveSince:    liveSince,
		SignalsSince: pgtype.Timestamptz{Time: now.Add(-providerSignalWindow), Valid: true},
		// Derived from the SAME policy value the floor is then applied from, so
		// the prefilter and the gate cannot drift apart.
		SettledBefore: pgtype.Timestamptz{Time: now.Add(-p.ResidencyFloor), Valid: true},
		// Which settled rows this tick gets to see. Only the settled tail is
		// affected: unreachable and refused incumbents sort ahead of the cursor's
		// key, not by it.
		ScanCursor: tick.scanCursor,
		RowLimit:   rotationScanLimit,
	})
	if err != nil {
		return 0, fmt.Errorf("coreapi: list rotation candidates: %w", err)
	}

	incumbents := rotationIncumbents(rows, now)
	// Re-sort in Go on the tier the policy actually derives. The scan's ORDER BY
	// is a heuristic that gets the likely-urgent rows inside the LIMIT; this is
	// what makes "the move budget goes to the broken mailboxes first" true
	// rather than likely.
	p.Prioritise(incumbents)

	plan := p.NewPlan()
	fleets := newRotationFleets()
	var moved int64
	for _, inc := range incumbents {
		if plan.Full() {
			break
		}
		fleet, err := fleets.measure(ctx, q, inc, liveSince, now)
		if err != nil {
			return moved, err
		}
		move, ok := plan.Consider(inc, fleet)
		if !ok {
			continue
		}
		applied, err := applyRotation(ctx, tick, move)
		if err != nil {
			return moved, err
		}
		if !applied {
			continue
		}
		moved++
		// Every cached measurement is now one mailbox out of date. Dropping the
		// whole cache is what keeps the next mailbox's comparison LIVE — it is
		// scored against a fleet that includes the move just made, not against
		// the picture that recommended it. The per-destination cap remains, for
		// the placements the SEND path is making concurrently, which no re-read
		// on this side can see.
		fleets.reset()
	}
	return moved, nil
}

// rotationIncumbents maps the scan's rows onto the policy's input. Counts are
// int64 on both sides; the only computed field is residency, and it is computed
// from the ONE `now` the whole tick shares so two mailboxes measured in the same
// tick are measured against the same instant.
func rotationIncumbents(rows []gen.ListRotationCandidatesRow, now time.Time) []fleetrotate.Incumbent {
	out := make([]fleetrotate.Incumbent, 0, len(rows))
	for _, r := range rows {
		out = append(out, fleetrotate.Incumbent{
			MailboxID:   r.MailboxID.String(),
			WorkspaceID: r.WorkspaceID.String(),
			Provider:    r.Provider,
			// warmup.RiskBandForLane is the one source of truth for this
			// mapping, exactly as AssignMailboxWorker uses it: a rotation is a
			// fresh placement decision, so it is made under the mailbox's
			// CURRENT band rather than the one stored on the assignment row.
			Band:        warmup.RiskBandForLane(r.Lane),
			WorkerID:    r.WorkerID,
			ResidentFor: now.Sub(r.AssignedAt.Time),
			Live:        r.IncumbentLive,
			OKEvents:    r.IncumbentOkEvents,
			BlockEvents: r.IncumbentBlockEvents,
		})
	}
	return out
}

// rotationFleetKey is everything ListPlacementCandidates' answer depends on.
// Two mailboxes sharing all three get one measurement, which is what keeps a
// blocked worker's hundred mailboxes from costing a hundred fleet scans.
type rotationFleetKey struct {
	workspace uuid.UUID
	band      string
	provider  string
}

// rotationFleets memoises the destination measurement WITHIN one tick, and only
// between moves: see rotateFleet's reset call for why the lifetime is that short.
type rotationFleets struct {
	cache map[rotationFleetKey][]fleetscore.Candidate
}

func newRotationFleets() *rotationFleets {
	return &rotationFleets{cache: map[rotationFleetKey][]fleetscore.Candidate{}}
}

func (f *rotationFleets) reset() { clear(f.cache) }

// measure reads the live fleet as placement would measure it for this mailbox —
// the same query, the same parameters — so "where would this mailbox be placed
// today?" and "is that better than where it is?" are answered by one
// measurement and one scorer rather than by two that can disagree.
//
// The trailing underscore keeps it off the `for` keyword; it is unexported and
// has exactly one caller.
func (f *rotationFleets) measure(ctx context.Context, q rotationStore, inc fleetrotate.Incumbent, liveSince pgtype.Timestamptz, now time.Time) ([]fleetscore.Candidate, error) {
	wsID, err := uuid.Parse(inc.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("coreapi: parse workspace id: %w", err)
	}
	key := rotationFleetKey{workspace: wsID, band: inc.Band, provider: inc.Provider}
	if cached, ok := f.cache[key]; ok {
		return cached, nil
	}

	rows, err := q.ListPlacementCandidates(ctx, gen.ListPlacementCandidatesParams{
		LiveSince:    liveSince,
		WorkspaceID:  wsID,
		Band:         inc.Band,
		Provider:     inc.Provider,
		SignalsSince: pgtype.Timestamptz{Time: now.Add(-providerSignalWindow), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("coreapi: list placement candidates for rotation: %w", err)
	}
	fleet := placementCandidates(rows)
	f.cache[key] = fleet
	return fleet, nil
}

// applyRotation performs one decided move and records it, and reports whether
// the move actually happened.
//
// It did NOT happen when the guarded UPDATE matches nothing, which means the
// mailbox is no longer on the worker this decision was made about — the send
// path re-placed it in the meantime. Nothing is logged in that case, and NOTHING
// IS COUNTED, and that is the point: a decision-log entry (or a metric sample)
// saying a mailbox moved from a worker it had already left would be a lie in the
// one table, and the one series, an operator trusts to explain where mail is
// coming from.
func applyRotation(ctx context.Context, tick rotationTick, move fleetrotate.Move) (bool, error) {
	q, recorder := tick.store, tick.recorder
	mbID, err := uuid.Parse(move.MailboxID)
	if err != nil {
		return false, fmt.Errorf("coreapi: parse mailbox id: %w", err)
	}
	wsID, err := uuid.Parse(move.WorkspaceID)
	if err != nil {
		return false, fmt.Errorf("coreapi: parse workspace id: %w", err)
	}

	_, err = q.RotateMailboxWorkerAssignment(ctx, gen.RotateMailboxWorkerAssignmentParams{
		MailboxID:    mbID,
		WorkspaceID:  wsID,
		FromWorkerID: move.FromWorkerID,
		ToWorkerID:   move.ToWorkerID,
		Band:         move.Band,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		slog.InfoContext(ctx, "fleet rotation skipped: the mailbox is no longer on the worker it was measured on",
			"mailbox_id", move.MailboxID, "from_worker_id", move.FromWorkerID, "tier", move.Tier.String())
		return false, nil
	case err != nil:
		return false, fmt.Errorf("coreapi: rotate mailbox worker assignment: %w", err)
	}

	slog.InfoContext(ctx, "mailbox rotated to a different worker",
		"mailbox_id", move.MailboxID, "workspace_id", move.WorkspaceID,
		"from_worker_id", move.FromWorkerID, "to_worker_id", move.ToWorkerID,
		"tier", move.Tier.String(), "reason", move.Reason.String())

	// Counted HERE, beside the log line and under the same condition: the write
	// matched. Tier.String() is the label vocabulary's one source of truth — see
	// metrics.FleetRotated, which deliberately does not restate it.
	tick.mtx.FleetRotated(move.Tier.String())

	// Best effort, like every other decision-log write (see recordDecision): a
	// decision that could not be logged is degraded observability, and failing
	// the tick over it would leave the rest of a blocked worker's mailboxes
	// stranded on it.
	if err := recorder.RecordFleetDecision(ctx, fleetdecision.Entry{
		Kind:        fleetdecision.KindRotate,
		WorkerID:    move.ToWorkerID,
		MailboxID:   move.MailboxID,
		WorkspaceID: move.WorkspaceID,
		Reason:      move.Reason,
		TriggeredBy: fleetdecision.Auto(fleetdecision.KindRotate),
	}); err != nil {
		slog.WarnContext(ctx, "fleet decision not recorded",
			"kind", fleetdecision.KindRotate, "mailbox_id", move.MailboxID, "err", err)
	}
	return true, nil
}

var (
	_ rotationStore        = (*gen.Queries)(nil)
	_ coreapi.FleetRotator = client{}
)
