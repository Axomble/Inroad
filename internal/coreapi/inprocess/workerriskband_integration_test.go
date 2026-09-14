//go:build integration

package inprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These integration tests exercise fleet F5 — risk-band segregation —
// against Postgres, on top of the routing fixtures in
// workerrouting_integration_test.go. Docker must be up.

// enrollWithLane enrolls mb in warmup and forces its lane directly with raw
// SQL rather than routing through the health evaluator's state machine: these
// tests only need the LANE value RiskBandForLane reads, not a
// production-realistic evidence trail (mirrors sentinelpairing_integration_
// test.go's setLane, adapted to the plain pool/queries fixture this file
// uses instead of warmupFixture).
func enrollWithLane(t *testing.T, ctx context.Context, q *gen.Queries, pool *pgxpool.Pool, ws, mb uuid.UUID, lane string) {
	t.Helper()
	if _, err := q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID: mb, WorkspaceID: ws, StartVolume: 5, MaxVolume: 20, RampIncrement: 1, ReplyRate: 0.2,
	}); err != nil {
		t.Fatalf("enroll warmup participant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE warmup_participants SET lane = $3 WHERE workspace_id = $1 AND mailbox_id = $2`,
		ws, mb, lane); err != nil {
		t.Fatalf("set lane %s on %s: %v", lane, mb, err)
	}
}

// TestAssignMailboxWorkerSegregatesByRiskBand: with two live workers, a
// healthy-lane mailbox and a degraded-lane (quarantine) mailbox never land on
// the same worker, EVEN WHEN plain "least loaded across the whole fleet"
// balancing would disagree. seg-a is seeded HEAVILY into the degraded band
// and seg-b LIGHTLY into the healthy band: a band-blind least-loaded pick
// would choose the lighter seg-b for a new degraded mailbox too (1 < 3) —
// only band-matching keeps it on seg-a, which is the property under test.
func TestAssignMailboxWorkerSegregatesByRiskBand(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing riskband "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "seg-a", "203.0.113.10"); err != nil {
		t.Fatalf("heartbeat seg-a: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "seg-b", "203.0.113.11"); err != nil {
		t.Fatalf("heartbeat seg-b: %v", err)
	}

	seedBand := func(worker, band string, n int) {
		for i := 0; i < n; i++ {
			mb := createRoutingMailbox(t, ctx, q, ws.ID)
			if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
				MailboxID: mb, WorkspaceID: ws.ID, WorkerID: worker, Band: band, LiveSince: liveSinceNow(),
			}); err != nil {
				t.Fatalf("seed %s/%s: %v", worker, band, err)
			}
		}
	}
	seedBand("seg-a", warmup.RiskBandDegraded, 3) // heavily loaded, but the ONLY degraded-committed worker
	seedBand("seg-b", warmup.RiskBandHealthy, 1)  // lightly loaded, and would win a band-blind pick

	d1 := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, d1, warmup.LaneQuarantine)
	if got, err := c.AssignMailboxWorker(ctx, d1.String(), ws.ID.String()); err != nil || got != "w:seg-a" {
		t.Fatalf("degraded mailbox = %q err=%v, want w:seg-a (must stay in-band despite being the heavier worker)", got, err)
	}

	h1 := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, h1, warmup.LaneHealthy)
	if got, err := c.AssignMailboxWorker(ctx, h1.String(), ws.ID.String()); err != nil || got != "w:seg-b" {
		t.Fatalf("healthy mailbox = %q err=%v, want w:seg-b", got, err)
	}
}

// TestAssignMailboxWorkerPromotionNeverTakesAnAlreadyCommittedWorker: the
// promotion path (requirement 3) may adopt ONLY a genuinely idle worker.
// p-a is LIGHTLY loaded but already committed to the degraded band, and p-c
// is HEAVILY loaded and also degraded-committed; NEITHER is idle, so a new
// healthy mailbox must be REFUSED — a band-blind (or "idle" implemented as
// merely least-loaded) picker would instead land it on p-a as the seemingly
// available option, which is exactly the mistake this proves does not happen.
func TestAssignMailboxWorkerPromotionNeverTakesAnAlreadyCommittedWorker(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing promotion "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "p-a", "203.0.113.20"); err != nil {
		t.Fatalf("heartbeat p-a: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "p-c", "203.0.113.22"); err != nil {
		t.Fatalf("heartbeat p-c: %v", err)
	}

	seed := func(worker string, n int) {
		for i := 0; i < n; i++ {
			mb := createRoutingMailbox(t, ctx, q, ws.ID)
			if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
				MailboxID: mb, WorkspaceID: ws.ID, WorkerID: worker, Band: warmup.RiskBandDegraded, LiveSince: liveSinceNow(),
			}); err != nil {
				t.Fatalf("seed %s: %v", worker, err)
			}
		}
	}
	seed("p-a", 1) // lightly loaded, but NOT idle
	seed("p-c", 3) // heavily loaded, also NOT idle

	h1 := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, h1, warmup.LaneHealthy)
	got, err := c.AssignMailboxWorker(ctx, h1.String(), ws.ID.String())
	if !errors.Is(err, coreapi.ErrNoBandCapacity) {
		t.Fatalf("healthy mailbox with no idle worker: got=%q err=%v, want ErrNoBandCapacity", got, err)
	}
	if got != "" {
		t.Fatalf("a refused promotion must not return a queue, got %q", got)
	}
}

// TestAssignMailboxWorkerRefusesWhenBandCapacityIsExhausted: strict
// segregation (requirement 2) — with every live worker already committed to
// the OTHER band, a mailbox whose band has neither a matching worker nor an
// idle one gets ErrNoBandCapacity, not a co-located placement, and nothing is
// persisted.
func TestAssignMailboxWorkerRefusesWhenBandCapacityIsExhausted(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing refuse "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "r-a", "203.0.113.30"); err != nil {
		t.Fatalf("heartbeat r-a: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "r-b", "203.0.113.31"); err != nil {
		t.Fatalf("heartbeat r-b: %v", err)
	}

	// Both live workers get committed to the healthy band directly (raw
	// InsertMailboxWorkerAssignment, not AssignMailboxWorker): the picker
	// itself prefers concentrating onto an already-committed worker over
	// promoting an idle one, so seeding through AssignMailboxWorker a second
	// time would just pile both healthy mailboxes onto the FIRST worker and
	// leave the second idle — this fixture needs BOTH workers genuinely
	// committed and NEITHER idle, which only a direct seed can force.
	for _, worker := range []string{"r-a", "r-b"} {
		mb := createRoutingMailbox(t, ctx, q, ws.ID)
		if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
			MailboxID: mb, WorkspaceID: ws.ID, WorkerID: worker, Band: warmup.RiskBandHealthy, LiveSince: liveSinceNow(),
		}); err != nil {
			t.Fatalf("seed %s: %v", worker, err)
		}
	}

	degraded := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, degraded, warmup.LaneQuarantine)
	got, err := c.AssignMailboxWorker(ctx, degraded.String(), ws.ID.String())
	if !errors.Is(err, coreapi.ErrNoBandCapacity) {
		t.Fatalf("assign with no band capacity: got=%q err=%v, want ErrNoBandCapacity", got, err)
	}
	if got != "" {
		t.Fatalf("a refused placement must not return a queue, got %q", got)
	}
	if worker, exists := storedAssignment(t, ctx, pool, degraded, ws.ID); exists {
		t.Fatalf("a refused placement must persist nothing, got worker_id=%q", worker)
	}
}

// TestAssignMailboxWorkerSelfHostBypassIgnoresBand is the single most
// important test in this file: with only ONE live worker (the self-host,
// RoleAll topology), a healthy and a degraded mailbox BOTH land on it without
// refusal. There is no second worker to segregate onto, so segregation must
// not apply — refusing here would silently stop self-host from sending.
func TestAssignMailboxWorkerSelfHostBypassIgnoresBand(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing selfhost "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "solo", "203.0.113.40"); err != nil {
		t.Fatalf("heartbeat solo: %v", err)
	}

	healthy := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, healthy, warmup.LaneHealthy)
	if got, err := c.AssignMailboxWorker(ctx, healthy.String(), ws.ID.String()); err != nil || got != "w:solo" {
		t.Fatalf("healthy mailbox on solo worker = %q err=%v, want w:solo", got, err)
	}

	degraded := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, degraded, warmup.LaneQuarantine)
	got, err := c.AssignMailboxWorker(ctx, degraded.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("degraded mailbox must NOT be refused when it is the only worker: %v", err)
	}
	if got != "w:solo" {
		t.Fatalf("degraded mailbox on solo worker = %q, want w:solo (self-host must be unaffected)", got)
	}
}

// TestAssignMailboxWorkerMigratesOnLaneDegradation: requirement 4. A mailbox
// assigned while healthy is moved to a degraded-band worker on its NEXT
// AssignMailboxWorker call after its lane degrades — the row updates in
// place, not a duplicate — and then stays put (no thrashing) as long as the
// band does not change again.
func TestAssignMailboxWorkerMigratesOnLaneDegradation(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing migrate "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "m-a", "203.0.113.50"); err != nil {
		t.Fatalf("heartbeat m-a: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "m-b", "203.0.113.51"); err != nil {
		t.Fatalf("heartbeat m-b: %v", err)
	}

	mb := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, mb, warmup.LaneHealthy)
	if got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || got != "w:m-a" {
		t.Fatalf("initial healthy assign = %q err=%v, want w:m-a", got, err)
	}

	// m-b is seeded HEAVILY into the degraded band — a band-blind least-loaded
	// re-pick would keep mb on the now-lighter m-a (1 assignment, still just
	// mb's own stale row) rather than move it to the heavier m-b (3). Only
	// band-matching correctly prefers m-b once mb's own band becomes degraded.
	for i := 0; i < 3; i++ {
		other := createRoutingMailbox(t, ctx, q, ws.ID)
		if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
			MailboxID: other, WorkspaceID: ws.ID, WorkerID: "m-b", Band: warmup.RiskBandDegraded, LiveSince: liveSinceNow(),
		}); err != nil {
			t.Fatalf("seed m-b: %v", err)
		}
	}

	// The mailbox's lane degrades (e.g. the health evaluator quarantined it).
	// m-a is still perfectly live — this must move the mailbox ANYWAY.
	if _, err := pool.Exec(ctx,
		`UPDATE warmup_participants SET lane = $3 WHERE workspace_id = $1 AND mailbox_id = $2`,
		ws.ID, mb, warmup.LaneQuarantine); err != nil {
		t.Fatalf("degrade lane: %v", err)
	}

	got, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("reassign after lane degradation: %v", err)
	}
	if got != "w:m-b" {
		t.Fatalf("post-degradation assign = %q, want w:m-b (moved to the degraded-committed worker despite being heavier)", got)
	}
	// Updated in place: exactly one row, now on the new worker with the new band.
	worker, exists := storedAssignment(t, ctx, pool, mb, ws.ID)
	if !exists || worker != "m-b" {
		t.Fatalf("stored assignment = (%q, exists=%t), want (m-b, true)", worker, exists)
	}
	var storedBand string
	if err := pool.QueryRow(ctx,
		"SELECT band FROM mailbox_worker_assignments WHERE mailbox_id = $1", mb).
		Scan(&storedBand); err != nil {
		t.Fatalf("read band: %v", err)
	}
	if storedBand != warmup.RiskBandDegraded {
		t.Fatalf("stored band = %q, want %q", storedBand, warmup.RiskBandDegraded)
	}

	// No thrashing: the lane hasn't changed again, so repeated resolves stay on
	// m-b — the idempotent same-band short-circuit, not a fresh pick each time.
	for i := 0; i < 3; i++ {
		if again, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String()); err != nil || again != "w:m-b" {
			t.Fatalf("resolve %d after migration = %q err=%v, want stable w:m-b", i, again, err)
		}
	}
}

// TestMailboxRiskBandDefaultsHealthyWithNoParticipant proves the "opting out
// of warmup costs nothing" rule at the placement layer: a mailbox with no
// warmup_participants row at all is treated as healthy-band, so it may be
// promotion-adopted onto (and share a worker with) other healthy mailboxes.
func TestMailboxRiskBandDefaultsHealthyWithNoParticipant(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing noparticipant "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "np-a", "203.0.113.60"); err != nil {
		t.Fatalf("heartbeat np-a: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "np-b", "203.0.113.61"); err != nil {
		t.Fatalf("heartbeat np-b: %v", err)
	}

	// h1 is an explicit warmup participant in the healthy lane; h2 is a plain
	// campaign-only mailbox that never enrolled in warmup at all.
	h1 := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, h1, warmup.LaneHealthy)
	if got, err := c.AssignMailboxWorker(ctx, h1.String(), ws.ID.String()); err != nil || got != "w:np-a" {
		t.Fatalf("seed healthy mailbox = %q err=%v, want w:np-a", got, err)
	}

	h2 := createRoutingMailbox(t, ctx, q, ws.ID)
	got, err := c.AssignMailboxWorker(ctx, h2.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("assign non-participant mailbox: %v", err)
	}
	if got != "w:np-a" {
		t.Fatalf("non-participant mailbox = %q, want w:np-a (defaults healthy, joins the healthy worker)", got)
	}
}
