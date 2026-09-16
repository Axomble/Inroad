//go:build integration

package inprocess

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/fleetdecision"
	"github.com/inroad/inroad/internal/platform/fleetrotate"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These exercise ROTATION — the gate that decides whether an already-placed
// mailbox should MOVE — against Postgres, on the routing fixtures in
// workerrouting_integration_test.go. Docker must be up.
//
// They are the half fleet F4 deliberately deferred. F4's placement keeps a live
// incumbent unconditionally, which is right for IP trust and leaves exactly one
// hole: a worker the provider has blocked keeps every mailbox already assigned
// to it. TestABlockedWorkerLosesItsMailboxesEvenThoughPlacementWouldKeepThem is
// that hole, asserted from both sides.

// assignedWorker reads which worker a mailbox is pinned to right now, ignoring
// liveness — the raw row, so a test can tell "moved" from "re-placed".
func assignedWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mb uuid.UUID) string {
	t.Helper()
	var worker string
	if err := pool.QueryRow(ctx,
		`SELECT worker_id FROM mailbox_worker_assignments WHERE mailbox_id = $1`, mb).Scan(&worker); err != nil {
		t.Fatalf("read assignment for %s: %v", mb, err)
	}
	return worker
}

// ageAssignment backdates a mailbox's residency clock, which is how a test
// reaches the opportunistic tier without waiting out the residency floor.
func ageAssignment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, mb uuid.UUID, age time.Duration) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE mailbox_worker_assignments SET assigned_at = now() - $2::interval WHERE mailbox_id = $1`,
		mb, age.String()); err != nil {
		t.Fatalf("age assignment for %s: %v", mb, err)
	}
}

// rotationDecisions reads the decision log for one mailbox through the query
// that exists to answer "why is this mailbox on this worker?".
func rotationDecisions(t *testing.T, ctx context.Context, q *gen.Queries, mb, ws uuid.UUID) []gen.FleetDecision {
	t.Helper()
	got, err := q.ListFleetDecisionsForMailbox(ctx, gen.ListFleetDecisionsForMailboxParams{
		MailboxID: mb, WorkspaceID: ws, RowLimit: 20,
	})
	if err != nil {
		t.Fatalf("read decisions for %s: %v", mb, err)
	}
	return got
}

// THE TEST THIS PR EXISTS FOR.
//
// It asserts the hole and the fix in one run, and the first half is what makes
// the second half mean anything: AssignMailboxWorker — today's code, unchanged
// by this PR — hands the mailbox straight back to the worker the provider is
// refusing, because a live incumbent wins unconditionally. Only the rotation
// pass moves it.
func TestABlockedWorkerLosesItsMailboxesEvenThoughPlacementWouldKeepThem(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Rotation blocked "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"rot-a-blocked", "rot-b-healthy"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.120", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: mb, WorkspaceID: ws.ID, WorkerID: "rot-a-blocked",
		Band: warmup.RiskBandHealthy, LiveSince: liveSinceNow(),
	}); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	// The provider now refuses that worker's address outright: blocks, and
	// nothing completed since. This is fleetscore.Eligible's definition of
	// unhealthy, and it is the ONE definition rotation uses.
	blockWorkerForProvider(t, ctx, c, "rot-a-blocked", "smtp", 0)

	// HALF ONE — the hole. Placement is asked the question and answers "stay",
	// which is today's behaviour and NOT a bug in placement: incumbency is the
	// heaviest force there on purpose.
	queueName, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("AssignMailboxWorker: %v", err)
	}
	if queueName != "w:rot-a-blocked" {
		t.Fatalf("placement returned %q, want w:rot-a-blocked — this test's premise is that placement KEEPS the mailbox "+
			"on the blocked worker; if that has changed, rotation is no longer the only thing that moves it", queueName)
	}

	// HALF TWO — the fix.
	moved, err := c.RotateMailboxWorkers(ctx)
	if err != nil {
		t.Fatalf("RotateMailboxWorkers: %v", err)
	}
	if moved != 1 {
		t.Fatalf("rotation moved %d mailboxes, want 1", moved)
	}
	if got := assignedWorker(t, ctx, pool, mb); got != "rot-b-healthy" {
		t.Fatalf("mailbox is on %q, want rot-b-healthy", got)
	}
	// And the send path now routes there, which is the operator-visible effect.
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:rot-b-healthy" {
		t.Fatalf("after rotation, placement = %q err=%v, want w:rot-b-healthy", got, err)
	}

	// The decision log says a rotation happened, names the destination, and —
	// because nothing was scored against a worker the gate had already excluded
	// — reports no comparison.
	decisions := rotationDecisions(t, ctx, q, mb, ws.ID)
	var rotations []gen.FleetDecision
	for _, d := range decisions {
		if d.Kind == string(fleetdecision.KindRotate) {
			rotations = append(rotations, d)
		}
	}
	if len(rotations) != 1 {
		t.Fatalf("logged %d rotate decisions, want 1 (all: %+v)", len(rotations), decisions)
	}
	got := rotations[0]
	if got.WorkerID == nil || *got.WorkerID != "rot-b-healthy" {
		t.Errorf("rotate decision names worker %v, want rot-b-healthy", got.WorkerID)
	}
	if got.TriggeredBy != string(fleetdecision.Auto(fleetdecision.KindRotate)) {
		t.Errorf("triggered_by = %q, want auto:rotate", got.TriggeredBy)
	}
	if !strings.HasPrefix(got.Reason, "forced: ") {
		t.Errorf("reason = %q, want a forced reason: the blocked incumbent was never scored against anything", got.Reason)
	}
	if strings.Contains(got.Reason, " over ") {
		t.Errorf("reason = %q, which reads as a score comparison that was never made", got.Reason)
	}
}

// The counter reaches the registry through the REAL composition path — the
// client's own *metrics.Metrics, not one handed to rotateFleet by a test. A tick
// that forgot to pass it would leave the series silent while every other
// assertion in this file still passed, which is the failure mode this covers:
// the unit tests drive rotateFleet directly and cannot see the wiring at all.
func TestARealRotationPassCountsTheMoveUnderItsTier(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	mtx := metrics.New()
	c := routingClientWithMetrics(pool, q, mtx)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Rotation metric "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"rot-metric-blocked", "rot-metric-ok"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.127", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: mb, WorkspaceID: ws.ID, WorkerID: "rot-metric-blocked",
		Band: warmup.RiskBandHealthy, LiveSince: liveSinceNow(),
	}); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	blockWorkerForProvider(t, ctx, c, "rot-metric-blocked", "smtp", 0)

	if moved, err := c.RotateMailboxWorkers(ctx); err != nil || moved != 1 {
		t.Fatalf("RotateMailboxWorkers moved %d (err=%v), want 1", moved, err)
	}

	families := metricstest.Scrape(t, mtx)
	if got := metricstest.CounterValue(families, "inroad_fleet_rotations_total",
		map[string]string{"tier": fleetrotate.TierUnhealthy.String()}); got != 1 {
		t.Errorf("rotations{tier=unhealthy} = %v, want 1", got)
	}
	// The blocked incumbent was never scored against anything, so this was not an
	// opportunistic move and must not appear as one.
	if got := metricstest.CounterValue(families, "inroad_fleet_rotations_total",
		map[string]string{"tier": fleetrotate.TierBalance.String()}); got != 0 {
		t.Errorf("rotations{tier=balance} = %v for a forced move, want 0", got)
	}
}

// Self-host, and the property that must never regress: one live worker means
// there is nowhere to rotate TO. A no-op, not an error, and no decision-log row
// — this runs every five minutes forever on every single-node deployment.
func TestRotationIsANoOpOnASingleWorkerFleet(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Rotation selfhost "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "rot-solo", "203.0.113.121", "hostname"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil {
		t.Fatalf("assign: %v", err)
	}
	// As broken as a sole worker can be: the provider refuses it, and it has
	// been there long past the residency floor.
	blockWorkerForProvider(t, ctx, c, "rot-solo", "smtp", 0)
	ageAssignment(t, ctx, pool, mb, 30*24*time.Hour)

	for i := 0; i < 2; i++ {
		moved, err := c.RotateMailboxWorkers(ctx)
		if err != nil {
			t.Fatalf("rotation on a one-worker fleet must not error: %v", err)
		}
		if moved != 0 {
			t.Fatalf("rotation moved %d mailboxes on a one-worker fleet, want 0", moved)
		}
	}
	if got := assignedWorker(t, ctx, pool, mb); got != "rot-solo" {
		t.Fatalf("mailbox is on %q, want rot-solo", got)
	}
	if d := rotationDecisions(t, ctx, q, mb, ws.ID); len(d) != 1 || d[0].Kind != string(fleetdecision.KindAssign) {
		t.Fatalf("decisions = %+v, want only the original placement — two rotation ticks must add nothing", d)
	}
}

// A healthy, evenly loaded fleet rotates nothing however long its mailboxes
// have been sitting there. This is the property that makes rotation safe to run
// every five minutes: incumbency is the heaviest force in placement on purpose,
// and a gate that fires on "nothing is wrong" would churn away the reputation
// that stability exists to protect.
func TestRotationMovesNothingOnAHealthyEvenFleet(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Rotation steady "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"rot-even-a", "rot-even-b", "rot-even-c"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.122", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
		seedAssignments(t, ctx, q, ws.ID, w, warmup.RiskBandHealthy, "smtp", 5)
	}
	// Every assignment long past the residency floor, so the floor is not what
	// is doing the work here — the margin is.
	if _, err := pool.Exec(ctx,
		`UPDATE mailbox_worker_assignments SET assigned_at = now() - interval '30 days'`); err != nil {
		t.Fatalf("age assignments: %v", err)
	}

	moved, err := c.RotateMailboxWorkers(ctx)
	if err != nil {
		t.Fatalf("RotateMailboxWorkers: %v", err)
	}
	if moved != 0 {
		t.Fatalf("rotation moved %d mailboxes on an even healthy fleet, want 0", moved)
	}
}

// The residency floor, against real rows: an incumbent crushed far past its
// target load and an empty worker beside it. Freshly placed, the mailbox stays.
// Backdated past the floor, the same fleet moves it. Nothing else changes
// between the two passes.
func TestTheResidencyFloorDelaysAnOpportunisticMoveWithoutCancellingIt(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Rotation floor "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"rot-full", "rot-empty"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.123", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}
	// Twice the scorer's target load on the incumbent, nothing on the
	// alternative: the widest gap the score can express.
	seedAssignments(t, ctx, q, ws.ID, "rot-full", warmup.RiskBandHealthy, "smtp", 80)

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: mb, WorkspaceID: ws.ID, WorkerID: "rot-full",
		Band: warmup.RiskBandHealthy, LiveSince: liveSinceNow(),
	}); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}

	if moved, err := c.RotateMailboxWorkers(ctx); err != nil || moved != 0 {
		t.Fatalf("rotation before the floor moved %d (err=%v), want 0", moved, err)
	}
	if got := assignedWorker(t, ctx, pool, mb); got != "rot-full" {
		t.Fatalf("mailbox moved to %q before the residency floor", got)
	}

	ageAssignment(t, ctx, pool, mb, fleetrotate.Default().ResidencyFloor+time.Hour)

	if moved, err := c.RotateMailboxWorkers(ctx); err != nil || moved == 0 {
		t.Fatalf("rotation past the floor moved %d (err=%v), want at least 1", moved, err)
	}
	if got := assignedWorker(t, ctx, pool, mb); got != "rot-empty" {
		t.Fatalf("mailbox is on %q, want rot-empty", got)
	}

	// A contested move REPORTS the comparison, because one was genuinely made.
	decisions := rotationDecisions(t, ctx, q, mb, ws.ID)
	if len(decisions) == 0 {
		t.Fatal("no decision logged for a move that happened")
	}
	reason := decisions[0].Reason
	if !strings.Contains(reason, " over ") {
		t.Errorf("reason = %q, want the score comparison that decided it", reason)
	}
	if !strings.Contains(reason, "rot-full") || !strings.Contains(reason, "rot-empty") {
		t.Errorf("reason = %q, want both workers named", reason)
	}
}

// A mailbox whose worker stopped heartbeating moves, and the log says it was
// forced. Distinct from the block above: an absent worker is not in the
// candidate list at all, so the tier cannot come from provider signals.
func TestAMailboxOnAWorkerThatStoppedHeartbeatingIsRotatedOff(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Rotation dead "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"rot-dead", "rot-alive-a", "rot-alive-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.124", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: mb, WorkspaceID: ws.ID, WorkerID: "rot-dead",
		Band: warmup.RiskBandHealthy, LiveSince: liveSinceNow(),
	}); err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
	// Aged rather than deleted: a deleted worker fails the liveness join
	// trivially, a stale one still joins and must be excluded on last_seen_at.
	killWorker(t, ctx, pool, "rot-dead", 2*workerLiveWindow)

	moved, err := c.RotateMailboxWorkers(ctx)
	if err != nil {
		t.Fatalf("RotateMailboxWorkers: %v", err)
	}
	if moved != 1 {
		t.Fatalf("rotation moved %d mailboxes, want 1", moved)
	}
	if got := assignedWorker(t, ctx, pool, mb); got == "rot-dead" {
		t.Fatal("mailbox is still pinned to the worker that stopped heartbeating")
	}
	decisions := rotationDecisions(t, ctx, q, mb, ws.ID)
	if len(decisions) == 0 || decisions[0].Kind != string(fleetdecision.KindRotate) {
		t.Fatalf("decisions = %+v, want a rotate entry", decisions)
	}
	if !strings.Contains(decisions[0].Reason, "heartbeat") {
		t.Errorf("reason = %q, want it to name the heartbeat as the cause", decisions[0].Reason)
	}
}

// Rotation is fleet-wide but every write is workspace-pinned. Two workspaces'
// mailboxes on the same blocked worker both move, and each decision-log row
// carries its own tenant — a rotation must never write a row naming another
// workspace's mailbox.
func TestRotationKeepsEachMailboxWithItsOwnWorkspace(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	first, err := q.CreateWorkspace(ctx, "Rotation tenant A "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace A: %v", err)
	}
	second, err := q.CreateWorkspace(ctx, "Rotation tenant B "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace B: %v", err)
	}
	for _, w := range []string{"rot-tenant-blocked", "rot-tenant-ok"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.125", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	mailboxes := map[uuid.UUID]uuid.UUID{} // mailbox -> its workspace
	for _, ws := range []uuid.UUID{first.ID, second.ID} {
		mb := createRoutingMailbox(t, ctx, q, ws)
		if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
			MailboxID: mb, WorkspaceID: ws, WorkerID: "rot-tenant-blocked",
			Band: warmup.RiskBandHealthy, LiveSince: liveSinceNow(),
		}); err != nil {
			t.Fatalf("seed assignment: %v", err)
		}
		mailboxes[mb] = ws
	}
	blockWorkerForProvider(t, ctx, c, "rot-tenant-blocked", "smtp", 0)

	moved, err := c.RotateMailboxWorkers(ctx)
	if err != nil {
		t.Fatalf("RotateMailboxWorkers: %v", err)
	}
	if moved != 2 {
		t.Fatalf("rotation moved %d mailboxes, want 2 (one per workspace)", moved)
	}
	for mb, ws := range mailboxes {
		if got := assignedWorker(t, ctx, pool, mb); got != "rot-tenant-ok" {
			t.Errorf("mailbox %s is on %q, want rot-tenant-ok", mb, got)
		}
		if d := rotationDecisions(t, ctx, q, mb, ws); len(d) != 1 {
			t.Errorf("mailbox %s has %d decisions under its own workspace, want 1", mb, len(d))
		}
		// ...and nothing under the OTHER workspace, which is what a row written
		// with the wrong tenant would look like.
		for other := range mailboxes {
			if mailboxes[other] == ws {
				continue
			}
			if d := rotationDecisions(t, ctx, q, mb, mailboxes[other]); len(d) != 0 {
				t.Errorf("mailbox %s has %d decisions under a FOREIGN workspace: %+v", mb, len(d), d)
			}
		}
	}
}

// One worker may absorb only its share of a tick, even when it is the only
// alternative and every mailbox in the fleet wants it. Without this, one pass
// empties a blocked worker onto a single destination that was measured before
// any of them arrived.
func TestOneDestinationAbsorbsOnlyItsShareOfATick(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Rotation cap "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"rot-cap-blocked", "rot-cap-only"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.126", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
	}

	p := fleetrotate.Default()
	seedAssignments(t, ctx, q, ws.ID, "rot-cap-blocked", warmup.RiskBandHealthy, "smtp", p.MaxMovesPerDestination*3)
	blockWorkerForProvider(t, ctx, c, "rot-cap-blocked", "smtp", 0)

	moved, err := c.RotateMailboxWorkers(ctx)
	if err != nil {
		t.Fatalf("RotateMailboxWorkers: %v", err)
	}
	if moved != int64(p.MaxMovesPerDestination) {
		t.Fatalf("one destination absorbed %d moves in a tick, want %d", moved, p.MaxMovesPerDestination)
	}

	var onDestination int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM mailbox_worker_assignments WHERE worker_id = 'rot-cap-only'`).Scan(&onDestination); err != nil {
		t.Fatalf("count destination assignments: %v", err)
	}
	if onDestination != p.MaxMovesPerDestination {
		t.Fatalf("destination holds %d mailboxes after one tick, want %d", onDestination, p.MaxMovesPerDestination)
	}
}
