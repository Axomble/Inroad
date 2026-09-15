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
	"github.com/inroad/inroad/internal/platform/fleetrotate"
	"github.com/inroad/inroad/internal/platform/fleetscore"
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
// qualifying assignments drains the broken ones first and the merely
// long-resident ones over subsequent ticks.
const rotationScanLimit = 40

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

// RotateMailboxWorkers runs one rotation tick. See the coreapi.FleetRotator
// interface doc for the contract.
func (c client) RotateMailboxWorkers(ctx context.Context) (int64, error) {
	return rotateFleet(ctx, c.q, c, fleetrotate.Default())
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
func rotateFleet(ctx context.Context, q rotationStore, recorder coreapi.FleetDecisionRecorder, p fleetrotate.Policy) (int64, error) {
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
		RowLimit:      rotationScanLimit,
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
		applied, err := applyRotation(ctx, q, recorder, move)
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
// path re-placed it in the meantime. Nothing is logged in that case, and that is
// the point: a decision-log entry saying a mailbox moved from a worker it had
// already left would be a lie in the one table an operator trusts to explain
// where mail is coming from.
func applyRotation(ctx context.Context, q rotationStore, recorder coreapi.FleetDecisionRecorder, move fleetrotate.Move) (bool, error) {
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
