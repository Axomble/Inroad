//go:build integration

package inprocess

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These integration tests cover fix round 1 on fleet F5 (PR #197 review):
//
//   - Important 1: the idle-promotion TOCTOU race, where two DIFFERENT
//     mailboxes of DIFFERENT bands could both adopt the SAME idle worker
//     because ON CONFLICT on mailbox_worker_assignments only guards two
//     callers racing over the SAME mailbox, never two callers racing over
//     the same WORKER for different mailboxes.
//   - Important 2: the convergence design — a real fleet, mixed by the
//     lane-derived migration-day reality, must keep sending (never
//     ErrNoBandCapacity fleet-wide) and must converge toward purity over
//     time, never stay mixed forever.
//
// Docker must be up. See workerrouting_integration_test.go /
// workerriskband_integration_test.go for the shared fixtures.

// distinctBandsOnWorker reports how many DISTINCT bands a worker's live
// assignments span. 0 = no assignments (idle), 1 = pure, 2 = mixed — the
// ground truth the race and convergence tests assert against, read directly
// off the table rather than through any query under test.
func distinctBandsOnWorker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workerID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		"SELECT count(DISTINCT band) FROM mailbox_worker_assignments WHERE worker_id = $1", workerID).
		Scan(&n); err != nil {
		t.Fatalf("count distinct bands on %s: %v", workerID, err)
	}
	return n
}

// TestAssignMailboxWorkerIdlePromotionRaceNeverMixesAWorker is fix-round-1's
// Important 1: fire concurrent placements of BOTH bands at ONE idle worker
// and assert it never ends up carrying both. Repeated across several rounds,
// each with its OWN fresh idle worker, to raise the odds of tripping a race
// that depends on two goroutines' SELECTs overlapping before either INSERTs
// — a single attempt is not reliable proof either way.
//
// The fleet needs a SECOND live worker beyond the idle one under contention.
// With only one live worker total, AssignMailboxWorker takes the self-host
// bypass (liveCount <= 1) and deliberately does not segregate at all — every
// band lands on that one worker by design, which would make "it ends up
// mixed" a false positive rather than proof of the race. The second worker,
// "race-committed", is pre-seeded MIXED (fix-round-1, Important 2's own
// last-resort tier) so it can absorb whichever band loses the race for the
// idle worker WITHOUT ever being refused — keeping every racer's outcome
// predictable (always a placement, never ErrNoBandCapacity) so a genuine bug
// cannot hide behind an "well, it could have been a legitimate refusal"
// excuse.
//
// A start barrier (closed channel) releases every goroutine at once rather
// than launching them in a loop, which tightens the window the unfixed code
// (pick and insert as two separate, non-transactional statements) needs to
// lose the race.
func TestAssignMailboxWorkerIdlePromotionRaceNeverMixesAWorker(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing race-idle "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	const rounds = 8
	const perBand = 4 // 4 healthy + 4 degraded racers per round
	for round := 0; round < rounds; round++ {
		idleWorker := fmt.Sprintf("race-idle-%d", round)
		committedWorker := fmt.Sprintf("race-committed-%d", round)
		if err := c.UpsertWorkerHeartbeat(ctx, idleWorker, "203.0.113.100", "hostname"); err != nil {
			t.Fatalf("round %d: heartbeat %s: %v", round, idleWorker, err)
		}
		if err := c.UpsertWorkerHeartbeat(ctx, committedWorker, "203.0.113.101", "hostname"); err != nil {
			t.Fatalf("round %d: heartbeat %s: %v", round, committedWorker, err)
		}
		for _, band := range []string{warmup.RiskBandHealthy, warmup.RiskBandDegraded} {
			decoy := createRoutingMailbox(t, ctx, q, ws.ID)
			if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
				MailboxID: decoy, WorkspaceID: ws.ID, WorkerID: committedWorker, Band: band, LiveSince: liveSinceNow(),
			}); err != nil {
				t.Fatalf("round %d: seed %s/%s: %v", round, committedWorker, band, err)
			}
		}

		type racer struct {
			mb   uuid.UUID
			lane string
		}
		var racers []racer
		for i := 0; i < perBand; i++ {
			h := createRoutingMailbox(t, ctx, q, ws.ID)
			enrollWithLane(t, ctx, q, pool, ws.ID, h, warmup.LaneHealthy)
			racers = append(racers, racer{h, warmup.LaneHealthy})
			d := createRoutingMailbox(t, ctx, q, ws.ID)
			enrollWithLane(t, ctx, q, pool, ws.ID, d, warmup.LaneQuarantine)
			racers = append(racers, racer{d, warmup.LaneQuarantine})
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, len(racers))
		for i, r := range racers {
			wg.Add(1)
			go func(i int, mb uuid.UUID) {
				defer wg.Done()
				<-start
				_, err := c.AssignMailboxWorker(ctx, mb.String(), ws.ID.String())
				errs[i] = err
			}(i, r.mb)
		}
		close(start)
		wg.Wait()

		for i, err := range errs {
			// The pre-seeded mixed committedWorker is a guaranteed fallback
			// for whichever band loses the race for idleWorker, so EVERY
			// racer must succeed outright — any error here is a real bug,
			// not a legitimate capacity refusal.
			if err != nil {
				t.Fatalf("round %d racer %d: unexpected error: %v", round, i, err)
			}
		}

		if got := distinctBandsOnWorker(t, ctx, pool, idleWorker); got > 1 {
			t.Fatalf("round %d: idle worker %s carries %d distinct bands, want at most 1 — the idle-promotion race mixed a pure worker", round, idleWorker, got)
		}
	}
}

// TestAssignMailboxWorkerSendsOnAFullyMixedFleetInsteadOfRefusing is
// fix-round-1's Important 2, "keeps sending" half. Every live worker starts
// ALREADY mixed — carrying both bands, seeded directly (not through
// AssignMailboxWorker, and not by truncating down to a fresh/pure fleet):
// this is what a real fleet looks like on this feature's first deploy, per
// the review's finding that a brand-new warmup_participants row defaults to
// a degraded band and placement was band-blind before this PR. A brand new
// mailbox of EITHER band must still be placed — never ErrNoBandCapacity —
// because refusing when the whole fleet is mixed would stop sending
// fleet-wide on deploy day, which is worse than an imperfectly segregated
// placement.
func TestAssignMailboxWorkerSendsOnAFullyMixedFleetInsteadOfRefusing(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing mixed-fleet "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	for _, w := range []string{"mix-a", "mix-b"} {
		if err := c.UpsertWorkerHeartbeat(ctx, w, "203.0.113.110", "hostname"); err != nil {
			t.Fatalf("heartbeat %s: %v", w, err)
		}
		// Pre-existing mix on EVERY live worker: one healthy row, one
		// degraded row, seeded directly — exactly what migration-day
		// backfill plus pre-F5 band-blind placement would have already left
		// behind, not something built up through the code under test.
		for _, band := range []string{warmup.RiskBandHealthy, warmup.RiskBandDegraded} {
			mb := createRoutingMailbox(t, ctx, q, ws.ID)
			if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
				MailboxID: mb, WorkspaceID: ws.ID, WorkerID: w, Band: band, LiveSince: liveSinceNow(),
			}); err != nil {
				t.Fatalf("seed %s/%s: %v", w, band, err)
			}
		}
	}
	// Confirm the fixture is genuinely mixed before asserting anything about
	// the code under test.
	for _, w := range []string{"mix-a", "mix-b"} {
		if got := distinctBandsOnWorker(t, ctx, pool, w); got != 2 {
			t.Fatalf("fixture bug: %s carries %d distinct bands, want 2 (mixed)", w, got)
		}
	}

	healthy := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, healthy, warmup.LaneHealthy)
	if got, err := c.AssignMailboxWorker(ctx, healthy.String(), ws.ID.String()); err != nil || got == "" {
		t.Fatalf("healthy mailbox on a fully mixed fleet: got=%q err=%v, want a real queue, not a refusal", got, err)
	}

	degraded := createRoutingMailbox(t, ctx, q, ws.ID)
	enrollWithLane(t, ctx, q, pool, ws.ID, degraded, warmup.LaneQuarantine)
	if got, err := c.AssignMailboxWorker(ctx, degraded.String(), ws.ID.String()); err != nil || got == "" {
		t.Fatalf("degraded mailbox on a fully mixed fleet: got=%q err=%v, want a real queue, not a refusal", got, err)
	}
}

// TestAssignMailboxWorkerConvergesOffAMixedWorkerOnceCapacityExists is
// fix-round-1's Important 2, "converges" half. A mailbox sitting on an
// already-mixed worker — WITHOUT any lane change of its own — moves onto a
// newly available pure/idle worker the next time it is resolved, proving the
// fleet drains toward purity over time rather than staying mixed forever.
func TestAssignMailboxWorkerConvergesOffAMixedWorkerOnceCapacityExists(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := routingClient(pool, q)
	resetRouting(t, ctx, pool)

	ws, err := q.CreateWorkspace(ctx, "Routing converge "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "mix-only", "203.0.113.120", "hostname"); err != nil {
		t.Fatalf("heartbeat mix-only: %v", err)
	}

	// A decoy healthy row, so mix-only is genuinely mixed once the tracked
	// mailbox's own degraded row joins it.
	decoy := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: decoy, WorkspaceID: ws.ID, WorkerID: "mix-only", Band: warmup.RiskBandHealthy, LiveSince: liveSinceNow(),
	}); err != nil {
		t.Fatalf("seed decoy: %v", err)
	}

	tracked := createRoutingMailbox(t, ctx, q, ws.ID)
	if _, err := q.InsertMailboxWorkerAssignment(ctx, gen.InsertMailboxWorkerAssignmentParams{
		MailboxID: tracked, WorkspaceID: ws.ID, WorkerID: "mix-only", Band: warmup.RiskBandDegraded, LiveSince: liveSinceNow(),
	}); err != nil {
		t.Fatalf("seed tracked: %v", err)
	}
	// The tracked mailbox's REAL lane matches the band it was seeded under —
	// this test isolates convergence driven by worker mixedness alone, not
	// by requirement 4's band-change path (already covered elsewhere).
	enrollWithLane(t, ctx, q, pool, ws.ID, tracked, warmup.LaneQuarantine)

	if got := distinctBandsOnWorker(t, ctx, pool, "mix-only"); got != 2 {
		t.Fatalf("fixture bug: mix-only carries %d distinct bands, want 2 (mixed)", got)
	}

	// Before any capacity is added: mix-only is the ONLY live worker, so the
	// tracked mailbox has nowhere better to go and must stay there (tier 3,
	// the only option) — not thrash, not get refused.
	got, err := c.AssignMailboxWorker(ctx, tracked.String(), ws.ID.String())
	if err != nil || got != "w:mix-only" {
		t.Fatalf("before capacity exists: got=%q err=%v, want w:mix-only (nowhere better to go yet)", got, err)
	}

	// Capacity appears: a second, genuinely idle live worker joins the fleet
	// (an operator scaling out, or another mailbox's load draining away).
	if err := c.UpsertWorkerHeartbeat(ctx, "clean-w", "203.0.113.121", "hostname"); err != nil {
		t.Fatalf("heartbeat clean-w: %v", err)
	}

	// The tracked mailbox's OWN band never changed — only capacity did. If
	// convergence relied solely on requirement 4's band-mismatch check, this
	// would incorrectly stay on mix-only forever.
	got, err = c.AssignMailboxWorker(ctx, tracked.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("after capacity exists: %v", err)
	}
	if got != "w:clean-w" {
		t.Fatalf("after capacity exists: got=%q, want w:clean-w — the mailbox must converge off the mixed worker", got)
	}

	// Idempotent from here: clean-w is now purely degraded (just the tracked
	// mailbox), so repeated resolves stay put rather than thrashing.
	for i := 0; i < 3; i++ {
		if again, err := c.AssignMailboxWorker(ctx, tracked.String(), ws.ID.String()); err != nil || again != "w:clean-w" {
			t.Fatalf("resolve %d after convergence = %q err=%v, want stable w:clean-w", i, again, err)
		}
	}
}
