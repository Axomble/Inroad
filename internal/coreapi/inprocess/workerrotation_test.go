package inprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/fleetdecision"
	"github.com/inroad/inroad/internal/platform/fleetrotate"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These exercise the rotation TICK — the order it considers mailboxes in, the
// move budget, the destination cache and what it does when a guarded write
// matches nothing. All of it against a fake rotationStore, so the control flow
// is pinned without a database; the SQL itself is exercised in
// workerrotation_integration_test.go.

// fakeRotationStore answers the four queries rotateFleet issues and records what
// it was asked. placementFleets is consumed one call at a time so a test can
// make the fleet CHANGE between lookups, which is how "the cache is dropped
// after a move" becomes observable rather than asserted about internals.
type fakeRotationStore struct {
	liveWorkers    int64
	candidates     []gen.ListRotationCandidatesRow
	placementFleet func(call int, arg gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow
	rotateErr      error

	scans          int
	placementCalls int
	placementArgs  []gen.ListPlacementCandidatesParams
	rotations      []gen.RotateMailboxWorkerAssignmentParams
}

func (f *fakeRotationStore) CountLiveWorkers(context.Context, pgtype.Timestamptz) (int64, error) {
	return f.liveWorkers, nil
}

func (f *fakeRotationStore) ListRotationCandidates(context.Context, gen.ListRotationCandidatesParams) ([]gen.ListRotationCandidatesRow, error) {
	f.scans++
	return f.candidates, nil
}

func (f *fakeRotationStore) ListPlacementCandidates(_ context.Context, arg gen.ListPlacementCandidatesParams) ([]gen.ListPlacementCandidatesRow, error) {
	f.placementArgs = append(f.placementArgs, arg)
	call := f.placementCalls
	f.placementCalls++
	if f.placementFleet == nil {
		return nil, nil
	}
	return f.placementFleet(call, arg), nil
}

func (f *fakeRotationStore) RotateMailboxWorkerAssignment(_ context.Context, arg gen.RotateMailboxWorkerAssignmentParams) (string, error) {
	if f.rotateErr != nil {
		return "", f.rotateErr
	}
	f.rotations = append(f.rotations, arg)
	return arg.ToWorkerID, nil
}

// fakeDecisionRecorder is the coreapi.FleetDecisionRecorder seam the tick writes
// its log through. It VALIDATES like the real one, so a test catches an entry
// the database would have rejected.
type fakeDecisionRecorder struct {
	entries []fleetdecision.Entry
	err     error
}

func (r *fakeDecisionRecorder) RecordFleetDecision(_ context.Context, e fleetdecision.Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	r.entries = append(r.entries, e)
	return r.err
}

// blockedCandidate is one assignment whose worker the provider is refusing: live
// (so the incumbent is not merely gone), with block events and nothing completed
// since — fleetscore.Eligible's own definition of unhealthy.
func blockedCandidate(worker string) gen.ListRotationCandidatesRow {
	return gen.ListRotationCandidatesRow{
		MailboxID:            uuid.New(),
		WorkspaceID:          uuid.New(),
		WorkerID:             worker,
		AssignedAt:           pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true},
		Provider:             "smtp",
		Lane:                 warmup.LaneHealthy,
		IncumbentLive:        true,
		IncumbentBlockEvents: 5,
	}
}

// deadCandidate is one assignment whose worker stopped heartbeating. Nothing
// about the provider explains it — an absent worker records no signals at all —
// so this is the only input that produces TierUnreachable.
func deadCandidate(worker string) gen.ListRotationCandidatesRow {
	row := blockedCandidate(worker)
	row.IncumbentLive = false
	row.IncumbentBlockEvents = 0
	return row
}

// twoWorkerFleet is the destination measurement: the blocked incumbent (which
// the scorer must drop) and one healthy alternative.
func twoWorkerFleet(blocked, healthy string) []gen.ListPlacementCandidatesRow {
	return []gen.ListPlacementCandidatesRow{
		{WorkerID: blocked, SmtpMailboxes: 1, BlockEvents: 5},
		{WorkerID: healthy, SmtpMailboxes: 4, OkEvents: 20},
	}
}

// Self-host, and the property that must never regress: with at most one live
// worker there is nowhere to rotate TO, so the tick reads nothing, writes
// nothing, logs no decision and does not fail.
func TestRotationOnASingleWorkerFleetScansNothingAtAll(t *testing.T) {
	store := &fakeRotationStore{liveWorkers: 1, candidates: []gen.ListRotationCandidatesRow{blockedCandidate("w-only")}}
	rec := &fakeDecisionRecorder{}

	moved, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: rec, policy: fleetrotate.Default()})
	if err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}
	if moved != 0 {
		t.Errorf("moved = %d, want 0", moved)
	}
	// The scan is the expensive part, and skipping it is the whole point: a
	// self-host deployment must not reconsider its entire fleet every tick.
	if store.scans != 0 {
		t.Errorf("scanned %d times on a one-worker fleet, want 0", store.scans)
	}
	if len(rec.entries) != 0 {
		t.Errorf("recorded %d decisions on a one-worker fleet, want 0", len(rec.entries))
	}
}

// The end-to-end shape of the fix: a blocked incumbent is moved, the write is
// pinned to the worker the decision was made about, and the log says a rotation
// happened and why.
func TestABlockedIncumbentIsMovedAndRecordedAsARotation(t *testing.T) {
	cand := blockedCandidate("w-blocked")
	store := &fakeRotationStore{
		liveWorkers: 2,
		candidates:  []gen.ListRotationCandidatesRow{cand},
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return twoWorkerFleet("w-blocked", "w-healthy")
		},
	}
	rec := &fakeDecisionRecorder{}

	moved, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: rec, policy: fleetrotate.Default()})
	if err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved = %d, want 1", moved)
	}

	if len(store.rotations) != 1 {
		t.Fatalf("issued %d rotations, want 1", len(store.rotations))
	}
	got := store.rotations[0]
	if got.FromWorkerID != "w-blocked" || got.ToWorkerID != "w-healthy" {
		t.Errorf("rotation %s -> %s, want w-blocked -> w-healthy", got.FromWorkerID, got.ToWorkerID)
	}
	if got.MailboxID != cand.MailboxID || got.WorkspaceID != cand.WorkspaceID {
		t.Errorf("rotation targeted (%s, %s), want the scanned pair (%s, %s)",
			got.MailboxID, got.WorkspaceID, cand.MailboxID, cand.WorkspaceID)
	}

	if len(rec.entries) != 1 {
		t.Fatalf("recorded %d decisions, want 1", len(rec.entries))
	}
	entry := rec.entries[0]
	if entry.Kind != fleetdecision.KindRotate {
		t.Errorf("kind = %q, want %q", entry.Kind, fleetdecision.KindRotate)
	}
	if entry.WorkerID != "w-healthy" {
		t.Errorf("entry names worker %q, want the destination w-healthy", entry.WorkerID)
	}
	if entry.TriggeredBy != fleetdecision.Auto(fleetdecision.KindRotate) {
		t.Errorf("triggered_by = %q, want auto:rotate", entry.TriggeredBy)
	}
	if entry.MailboxID != cand.MailboxID.String() || entry.WorkspaceID != cand.WorkspaceID.String() {
		t.Errorf("entry names (%s, %s), want the rotated pair", entry.MailboxID, entry.WorkspaceID)
	}
}

// The destination is measured with the SAME parameters placement would use for
// this mailbox — its own workspace, its CURRENT band and its provider — because
// a rotation that preferred workers placement would not would fight the placer
// forever.
func TestTheDestinationIsMeasuredAsPlacementWouldMeasureIt(t *testing.T) {
	cand := blockedCandidate("w-blocked")
	cand.Provider = "gmail"
	// A degraded warmup lane, so a band read off the LANE and a band defaulted
	// to "healthy" are distinguishable.
	cand.Lane = warmup.LaneWatch
	store := &fakeRotationStore{
		liveWorkers: 2,
		candidates:  []gen.ListRotationCandidatesRow{cand},
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return twoWorkerFleet("w-blocked", "w-healthy")
		},
	}

	if _, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: &fakeDecisionRecorder{}, policy: fleetrotate.Default()}); err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}
	if len(store.placementArgs) != 1 {
		t.Fatalf("measured the fleet %d times, want 1", len(store.placementArgs))
	}
	arg := store.placementArgs[0]
	if arg.WorkspaceID != cand.WorkspaceID {
		t.Errorf("measured for workspace %s, want the mailbox's own %s", arg.WorkspaceID, cand.WorkspaceID)
	}
	if arg.Provider != "gmail" {
		t.Errorf("measured for provider %q, want gmail", arg.Provider)
	}
	if arg.Band != warmup.RiskBandDegraded {
		t.Errorf("measured for band %q, want degraded — the band comes from the mailbox's CURRENT lane", arg.Band)
	}
	if len(store.rotations) != 1 || store.rotations[0].Band != warmup.RiskBandDegraded {
		t.Errorf("rotation wrote band %+v, want the band the decision was scored under", store.rotations)
	}
}

// Two mailboxes with the same (workspace, band, provider) share ONE measurement
// while nothing has moved — otherwise a blocked worker's hundred mailboxes cost
// a hundred fleet scans — but the cache is dropped as soon as a move lands, so
// the next comparison is made against a fleet that INCLUDES it.
func TestTheDestinationMeasurementIsReReadAfterEveryMove(t *testing.T) {
	ws := uuid.New()
	first, second := blockedCandidate("w-blocked"), blockedCandidate("w-blocked")
	first.WorkspaceID, second.WorkspaceID = ws, ws

	store := &fakeRotationStore{
		liveWorkers: 2,
		candidates:  []gen.ListRotationCandidatesRow{first, second},
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return twoWorkerFleet("w-blocked", "w-healthy")
		},
	}

	moved, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: &fakeDecisionRecorder{}, policy: fleetrotate.Default()})
	if err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}
	if moved != 2 {
		t.Fatalf("moved = %d, want 2", moved)
	}
	// One lookup for the first mailbox, then a fresh one for the second because
	// the first move invalidated it. A cache that survived the move would leave
	// this at 1 and score the second mailbox against a fleet missing a mailbox
	// it had itself just placed.
	if store.placementCalls != 2 {
		t.Fatalf("measured the fleet %d times for 2 moves, want 2 — the cache must be dropped after a move", store.placementCalls)
	}
}

// A mailbox that left its worker between the scan and the write is not rotated
// and NOT logged. The guarded UPDATE matching nothing means the send path
// re-placed it on fresher facts; recording "moved from w-old" would name a
// worker it had already left.
func TestAMailboxThatMovedUnderTheTickIsNeitherRotatedNorLogged(t *testing.T) {
	store := &fakeRotationStore{
		liveWorkers: 2,
		candidates:  []gen.ListRotationCandidatesRow{blockedCandidate("w-blocked")},
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return twoWorkerFleet("w-blocked", "w-healthy")
		},
		rotateErr: pgx.ErrNoRows,
	}
	rec := &fakeDecisionRecorder{}

	moved, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: rec, policy: fleetrotate.Default()})
	if err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}
	if moved != 0 {
		t.Errorf("moved = %d, want 0 — the guarded update matched nothing", moved)
	}
	if len(rec.entries) != 0 {
		t.Errorf("recorded %d decisions for a move that did not happen: %+v", len(rec.entries), rec.entries)
	}
}

// A decision the log refused is degraded observability, never a stranded
// mailbox: the move stands and the tick keeps going.
func TestALogWriteFailureDoesNotUndoOrStopTheRotation(t *testing.T) {
	store := &fakeRotationStore{
		liveWorkers: 2,
		candidates:  []gen.ListRotationCandidatesRow{blockedCandidate("w-blocked")},
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return twoWorkerFleet("w-blocked", "w-healthy")
		},
	}
	rec := &fakeDecisionRecorder{err: errors.New("decision log unavailable")}

	moved, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: rec, policy: fleetrotate.Default()})
	if err != nil {
		t.Fatalf("rotateFleet must not fail over a log write: %v", err)
	}
	if moved != 1 || len(store.rotations) != 1 {
		t.Errorf("moved = %d with %d rotations, want 1 and 1", moved, len(store.rotations))
	}
}

// A database failure on the move itself is NOT swallowed: it is a real failure
// of the tick, and asynq retrying it is the right answer.
func TestARotationWriteFailureFailsTheTick(t *testing.T) {
	sentinel := errors.New("connection reset")
	store := &fakeRotationStore{
		liveWorkers: 2,
		candidates:  []gen.ListRotationCandidatesRow{blockedCandidate("w-blocked")},
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return twoWorkerFleet("w-blocked", "w-healthy")
		},
		rotateErr: sentinel,
	}

	if _, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: &fakeDecisionRecorder{}, policy: fleetrotate.Default()}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
}

// The move budget goes to the mailboxes that are actually broken. The scan's
// ORDER BY is only a heuristic, so the tick re-sorts on the tier it derives
// itself — without that, a tick full of merely-long-resident mailboxes could
// spend its whole budget before reaching a blocked one.
func TestTheMoveBudgetIsSpentOnUrgentMailboxesFirst(t *testing.T) {
	p := fleetrotate.Default()
	ws := uuid.New()

	// Deliberately scanned in the WORST order: every settled mailbox first, the
	// blocked one last, so only the re-sort can rescue it.
	var rows []gen.ListRotationCandidatesRow
	for i := 0; i < p.MaxMoves+5; i++ {
		settled := blockedCandidate("w-crushed")
		settled.WorkspaceID = ws
		settled.IncumbentBlockEvents, settled.IncumbentOkEvents = 0, 50 // healthy: TierBalance
		settled.AssignedAt = pgtype.Timestamptz{Time: time.Now().Add(-2 * p.ResidencyFloor), Valid: true}
		rows = append(rows, settled)
	}
	urgent := blockedCandidate("w-blocked")
	urgent.WorkspaceID = ws
	rows = append(rows, urgent)

	store := &fakeRotationStore{
		liveWorkers: 3,
		candidates:  rows,
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return []gen.ListPlacementCandidatesRow{
				{WorkerID: "w-blocked", SmtpMailboxes: 1, BlockEvents: 5},
				// Over target, so the settled mailboxes have somewhere better to
				// go and genuinely compete for the budget.
				{WorkerID: "w-crushed", SmtpMailboxes: 90, OkEvents: 20},
				{WorkerID: "w-roomy", SmtpMailboxes: 1, OkEvents: 20},
			}
		},
	}

	if _, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: &fakeDecisionRecorder{}, policy: p}); err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}
	if len(store.rotations) == 0 {
		t.Fatal("nothing moved at all")
	}
	if from := store.rotations[0].FromWorkerID; from != "w-blocked" {
		t.Fatalf("first move came off %q, want w-blocked — the urgent mailbox must get the budget first", from)
	}
}

// One tick, one budget. Whatever the scan returns, a pass may not discard more
// of the fleet's accumulated IP history than the policy allows.
func TestATickNeverExceedsTheMoveBudget(t *testing.T) {
	p := fleetrotate.Default()
	ws := uuid.New()

	var rows []gen.ListRotationCandidatesRow
	for i := 0; i < p.MaxMoves*2; i++ {
		row := blockedCandidate("w-blocked")
		row.WorkspaceID = ws
		rows = append(rows, row)
	}
	// Enough healthy destinations that the per-destination cap cannot be what
	// binds first.
	fleet := []gen.ListPlacementCandidatesRow{{WorkerID: "w-blocked", SmtpMailboxes: 1, BlockEvents: 5}}
	for _, id := range []string{"w-a", "w-b", "w-c", "w-d", "w-e", "w-f", "w-g", "w-h"} {
		fleet = append(fleet, gen.ListPlacementCandidatesRow{WorkerID: id, OkEvents: 20})
	}

	store := &fakeRotationStore{
		liveWorkers: int64(len(fleet)),
		candidates:  rows,
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return fleet
		},
	}

	moved, err := rotateFleet(context.Background(), rotationTick{store: store, recorder: &fakeDecisionRecorder{}, policy: p})
	if err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}
	if moved != int64(p.MaxMoves) {
		t.Fatalf("moved %d of %d candidates, want the budget %d", moved, len(rows), p.MaxMoves)
	}
}

// Each move lands on the series for the tier that justified it. An operator
// seeing churn asks WHY, and the three tiers have three different answers and
// three different fixes — a host that died, a provider refusing an egress IP, or
// a score margin tuned too loose. One undifferentiated counter could not tell
// them apart.
func TestEveryRotationIsCountedUnderTheTierThatJustifiedIt(t *testing.T) {
	ws := uuid.New()
	blocked, dead := blockedCandidate("w-blocked"), deadCandidate("w-dead")
	blocked.WorkspaceID, dead.WorkspaceID = ws, ws

	store := &fakeRotationStore{
		liveWorkers: 3,
		candidates:  []gen.ListRotationCandidatesRow{blocked, dead},
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			// w-dead is absent from the live fleet, which is exactly what makes
			// its mailbox unreachable rather than merely unhealthy.
			return twoWorkerFleet("w-blocked", "w-healthy")
		},
	}
	mtx := metrics.New()

	moved, err := rotateFleet(context.Background(), rotationTick{
		store: store, recorder: &fakeDecisionRecorder{}, mtx: mtx, policy: fleetrotate.Default(),
	})
	if err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}
	if moved != 2 {
		t.Fatalf("moved = %d, want 2", moved)
	}

	families := metricstest.Scrape(t, mtx)
	for tier, want := range map[string]float64{
		fleetrotate.TierUnhealthy.String():   1,
		fleetrotate.TierUnreachable.String(): 1,
		// Nothing opportunistic happened, so that series must stay empty: a
		// rotation counter that lumped a forced move in with a repack would make
		// the one tier worth alerting on unreadable.
		fleetrotate.TierBalance.String(): 0,
	} {
		got := metricstest.CounterValue(families, "inroad_fleet_rotations_total", map[string]string{"tier": tier})
		if got != want {
			t.Errorf("rotations{tier=%q} = %v, want %v", tier, got, want)
		}
	}
}

// A decision the guarded UPDATE refused is NOT a rotation and must not be
// counted. This is the same rule the decision log follows (nothing is written
// for a move that did not happen), and counting decisions instead of moves would
// leave this counter permanently disagreeing with both that log and the
// assignment table.
func TestARotationThatLostTheRaceIsNotCounted(t *testing.T) {
	store := &fakeRotationStore{
		liveWorkers: 2,
		candidates:  []gen.ListRotationCandidatesRow{blockedCandidate("w-blocked")},
		placementFleet: func(int, gen.ListPlacementCandidatesParams) []gen.ListPlacementCandidatesRow {
			return twoWorkerFleet("w-blocked", "w-healthy")
		},
		rotateErr: pgx.ErrNoRows,
	}
	mtx := metrics.New()

	if _, err := rotateFleet(context.Background(), rotationTick{
		store: store, recorder: &fakeDecisionRecorder{}, mtx: mtx, policy: fleetrotate.Default(),
	}); err != nil {
		t.Fatalf("rotateFleet: %v", err)
	}

	families := metricstest.Scrape(t, mtx)
	for _, tier := range []string{
		fleetrotate.TierUnhealthy.String(),
		fleetrotate.TierUnreachable.String(),
		fleetrotate.TierBalance.String(),
	} {
		if got := metricstest.CounterValue(families, "inroad_fleet_rotations_total", map[string]string{"tier": tier}); got != 0 {
			t.Errorf("rotations{tier=%q} = %v after a move that never happened, want 0", tier, got)
		}
	}
}
