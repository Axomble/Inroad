package fleetrotate

import (
	"strings"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/platform/fleetscore"
)

// settled is an incumbent that has been in place long past the residency floor
// and whose worker is live and healthy — the neutral fixture every test below
// perturbs in exactly ONE dimension, so a changed verdict can only come from the
// dimension under test.
func settled(worker string) Incumbent {
	return Incumbent{
		MailboxID:   "mb-1",
		WorkspaceID: "ws-1",
		Provider:    "smtp",
		Band:        "healthy",
		WorkerID:    worker,
		ResidentFor: 30 * 24 * time.Hour,
		Live:        true,
		OKEvents:    100,
	}
}

// worker builds a live candidate carrying n SMTP mailboxes of other workspaces
// in the incoming mailbox's own band, so load is the only thing separating two
// of them. Mirrors fleetscore's own smtpWorker fixture for the same reason.
func worker(id string, n int) fleetscore.Candidate {
	return fleetscore.Candidate{WorkerID: id, SMTPMailboxes: n}
}

// consider runs one incumbent through a fresh plan and reports the decision.
func consider(t *testing.T, p Policy, inc Incumbent, fleet ...fleetscore.Candidate) (Move, bool) {
	t.Helper()
	return p.NewPlan().Consider(inc, fleet)
}

// THE TEST THIS WHOLE FEATURE EXISTS FOR. A worker the provider is refusing
// outright keeps every mailbox already assigned to it, forever, because
// placement's health gate only guards NEW placements. Eviction is rotation's
// job and this is it.
func TestAMailboxOnAWorkerTheProviderIsBlockingIsMoved(t *testing.T) {
	p := Default()
	inc := settled("a-blocked")
	// The same shape fleetscore.Eligible calls unhealthy: recent refusals to
	// talk to this address, and nothing completed since.
	inc.OKEvents, inc.BlockEvents = 0, 7

	// The blocked incumbent is still LIVE, so it is in the candidate list — and
	// it is emptier than the alternative, so a scorer that ignored health would
	// keep the mailbox exactly where it is.
	blocked := fleetscore.Candidate{WorkerID: "a-blocked", SMTPMailboxes: 1, ProviderBlockEvents: 7}
	move, ok := consider(t, p, inc, blocked, worker("b-healthy", 20))
	if !ok {
		t.Fatalf("a mailbox on a worker the provider is blocking must move; got no move")
	}
	if move.ToWorkerID != "b-healthy" {
		t.Errorf("moved to %q, want b-healthy", move.ToWorkerID)
	}
	if move.Tier != TierUnhealthy {
		t.Errorf("tier = %v, want %v", move.Tier, TierUnhealthy)
	}
}

// A worker that stopped heartbeating routes to a queue no process consumes.
// That is the other urgent tier, and it is distinguishable from the block so an
// operator reading the log knows which failure they are looking at.
func TestAMailboxOnAWorkerThatStoppedHeartbeatingIsMoved(t *testing.T) {
	p := Default()
	inc := settled("a-dead")
	inc.Live = false

	// A worker outside the live window is not in the candidate list at all,
	// which is why the tier cannot be derived from provider signals.
	move, ok := consider(t, p, inc, worker("b-live", 20))
	if !ok {
		t.Fatal("a mailbox pinned to a worker that stopped heartbeating must move")
	}
	if move.Tier != TierUnreachable {
		t.Errorf("tier = %v, want %v", move.Tier, TierUnreachable)
	}
	if move.ToWorkerID != "b-live" {
		t.Errorf("moved to %q, want b-live", move.ToWorkerID)
	}
}

// Urgency is what makes the residency floor an exception rather than a ceiling:
// a mailbox placed sixty seconds ago onto a worker the provider then blocked
// must not be held there by a rule that exists to stop opportunistic churn.
func TestUrgentTiersBypassTheResidencyFloor(t *testing.T) {
	p := Default()
	for _, tc := range []struct {
		name string
		mut  func(*Incumbent)
	}{
		{"blocked", func(i *Incumbent) { i.OKEvents, i.BlockEvents = 0, 3 }},
		{"unreachable", func(i *Incumbent) { i.Live = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inc := settled("a-bad")
			inc.ResidentFor = time.Minute
			tc.mut(&inc)
			if _, ok := consider(t, p, inc, fleetscore.Candidate{WorkerID: "a-bad", ProviderBlockEvents: 3}, worker("b-good", 5)); !ok {
				t.Fatal("an urgent move must not be held back by the residency floor")
			}
		})
	}
}

// The floor itself: nothing is wrong, the destination is wildly better, and the
// mailbox has only just arrived. It stays. IP trust accrues per (mailbox, IP)
// pair and a placement that has not settled has nothing to weigh against it.
func TestAnOpportunisticMoveIsRefusedBeforeTheResidencyFloor(t *testing.T) {
	p := Default()
	inc := settled("a-full")
	// An ABSOLUTE duration, not one derived from p.ResidencyFloor: a fixture
	// computed from the number under test goes vacuous the moment that number is
	// zeroed, which is the one mutation this test has to catch.
	inc.ResidentFor = time.Minute

	// Deliberately the most one-sided fleet the scorer can express: the
	// incumbent is at twice its target load, the destination is empty.
	crushed := worker("a-full", int(p.Score.TargetLoad)*2)
	empty := worker("b-empty", 0)
	if move, ok := consider(t, p, inc, crushed, empty); ok {
		t.Fatalf("moved to %s before the residency floor; an opportunistic move must wait", move.ToWorkerID)
	}
}

// Past the floor the same fleet DOES justify a move: the floor delays an
// opportunistic move, it does not cancel it. Without this, the test above would
// pass just as well against a rotation that never moves anything.
func TestAnOpportunisticMoveHappensOnceTheFloorIsPast(t *testing.T) {
	p := Default()
	inc := settled("a-full")
	inc.ResidentFor = p.ResidencyFloor

	crushed := worker("a-full", int(p.Score.TargetLoad)*2)
	empty := worker("b-empty", 0)
	move, ok := consider(t, p, inc, crushed, empty)
	if !ok {
		t.Fatal("past the residency floor, an over-target incumbent and an empty destination must justify a move")
	}
	if move.Tier != TierBalance {
		t.Errorf("tier = %v, want %v — nothing is WRONG with the incumbent, it is merely worse", move.Tier, TierBalance)
	}
}

// The margin. A destination that scores trivially better is not a reason to
// discard IP trust — and "trivially" has to be measured against what the score
// can express, not against zero.
func TestAnOpportunisticMoveNeedsMoreThanTheMargin(t *testing.T) {
	p := Default()
	inc := settled("a-here")

	// The incumbent is scored WITHOUT the mailbox it would be losing, so ten
	// here against eight there is one real SMTP mailbox of advantage to the
	// destination — a genuine, measurable improvement worth 1/TargetLoad of the
	// headroom range, and nowhere near a reason to discard IP trust.
	if move, ok := consider(t, p, inc, worker("a-here", 10), worker("b-there", 8)); ok {
		t.Fatalf("moved to %s for one mailbox of difference; the margin must forbid it", move.ToWorkerID)
	}
}

// Staying and moving are only comparable if the incumbent is scored WITHOUT the
// mailbox it would be losing: every destination is scored with the mailbox
// arriving, so charging the incumbent for it as well counts it twice and tilts
// every comparison toward moving by one mailbox's worth.
//
// At Default().MinScoreMargin that tilt is ~0.25 of a 10-point margin and could
// never flip a verdict, which is exactly why this test shrinks the margin: the
// bias is real, permanent, and in the one direction this package exists to
// resist, so it must be caught by something rather than left to the margin's
// slack.
func TestTheIncumbentIsScoredWithoutTheMailboxItWouldLose(t *testing.T) {
	p := Default()
	p.MinScoreMargin = 0.2 // smaller than one SMTP mailbox is worth (0.25)

	// Ten here against nine there. Counted honestly the incumbent keeps nine and
	// the destination would hold ten, so staying is at least as good and nothing
	// moves. Counted twice the incumbent looks like eleven and loses.
	if move, ok := consider(t, p, settled("a-here"), worker("a-here", 10), worker("b-there", 9)); ok {
		t.Fatalf("moved to %s; the incumbent was charged for the mailbox it is giving up", move.ToWorkerID)
	}
}

// A contested move must report the comparison it ACTUALLY made — and a forced
// one must not report a comparison at all. The decision log's whole rule.
func TestOnlyAContestedMoveReportsAScoreComparison(t *testing.T) {
	p := Default()

	contested := settled("a-full")
	move, ok := consider(t, p, contested, worker("a-full", int(p.Score.TargetLoad)*2), worker("b-empty", 0))
	if !ok {
		t.Fatal("expected the over-target incumbent to lose its mailbox")
	}
	reason := move.Reason.String()
	if !strings.Contains(reason, " over ") {
		t.Errorf("contested reason = %q, want the comparison that decided it", reason)
	}
	if !strings.Contains(reason, "a-full") || !strings.Contains(reason, "b-empty") {
		t.Errorf("contested reason = %q, want both workers named", reason)
	}

	forced := settled("a-dead")
	forced.Live = false
	move, ok = consider(t, p, forced, worker("b-live", 3))
	if !ok {
		t.Fatal("expected the unreachable incumbent to lose its mailbox")
	}
	if reason := move.Reason.String(); strings.Contains(reason, " over ") {
		t.Errorf("forced reason = %q, want no comparison: nothing was scored against the dead worker", reason)
	}
	if reason := move.Reason.String(); !strings.HasPrefix(reason, "forced: ") {
		t.Errorf("forced reason = %q, want it to say it was forced", reason)
	}
}

// A healthy, evenly loaded fleet must move nothing at all. Incumbency is the
// heaviest force in placement on purpose; a rotation that fires on a fleet with
// nothing wrong with it churns away the reputation F4 exists to protect.
func TestAnEvenHealthyFleetMovesNothing(t *testing.T) {
	p := Default()
	fleet := []fleetscore.Candidate{worker("a", 10), worker("b", 10), worker("c", 10)}
	plan := p.NewPlan()
	for _, id := range []string{"a", "b", "c"} {
		if move, ok := plan.Consider(settled(id), fleet); ok {
			t.Fatalf("mailbox on %s moved to %s; an even healthy fleet must rotate nothing", id, move.ToWorkerID)
		}
	}
}

// The fleet-wide budget. A tick may not discard more of the fleet's IP history
// than the budget allows, however many mailboxes qualify.
func TestATickMovesAtMostTheMoveBudget(t *testing.T) {
	p := Default()
	// Enough destinations that the per-destination cap cannot be what binds.
	fleet := []fleetscore.Candidate{worker("a-dead-stand-in", 0)}
	for _, id := range []string{"b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		fleet = append(fleet, worker(id, 0))
	}

	plan := p.NewPlan()
	moved := 0
	for i := 0; i < p.MaxMoves*3; i++ {
		inc := settled("gone")
		inc.Live = false // urgent, so neither floor nor margin can be the limiter
		if _, ok := plan.Consider(inc, fleet); ok {
			moved++
		}
	}
	if moved != p.MaxMoves {
		t.Fatalf("moved %d of %d attempts, want the budget %d", moved, p.MaxMoves*3, p.MaxMoves)
	}
}

// The per-destination cap. Candidate scores are read once per destination
// lookup, so within one tick the emptiest worker looks equally empty to every
// mailbox being considered — without this cap a single pass fills it.
func TestOneDestinationAbsorbsAtMostItsPerTickShare(t *testing.T) {
	p := Default()
	// ONE destination, deliberately: every urgent mailbox would otherwise pile
	// onto it.
	fleet := []fleetscore.Candidate{worker("b-only", 0)}

	plan := p.NewPlan()
	moved := 0
	for i := 0; i < p.MaxMoves; i++ {
		inc := settled("gone")
		inc.Live = false
		if _, ok := plan.Consider(inc, fleet); ok {
			moved++
		}
	}
	// Both assertions, and the second is the one that catches a per-destination
	// cap widened to the tick budget: that is not a cap, it is the budget with
	// an extra name, and comparing only against p.MaxMovesPerDestination would
	// pass just as happily.
	if moved != p.MaxMovesPerDestination {
		t.Fatalf("one destination absorbed %d moves, want at most %d", moved, p.MaxMovesPerDestination)
	}
	if moved >= p.MaxMoves {
		t.Fatalf("one destination absorbed the whole tick budget (%d of %d); the per-destination cap bound nothing", moved, p.MaxMoves)
	}
}

// When the budget binds, it must be spent on the mailboxes that are actually
// broken. An opportunistic move that displaces an urgent one is the budget
// working exactly backwards.
func TestThePrioritiserPutsUrgentMailboxesFirst(t *testing.T) {
	balance := settled("a")
	blocked := settled("b")
	blocked.OKEvents, blocked.BlockEvents = 0, 1
	dead := settled("c")
	dead.Live = false

	in := []Incumbent{balance, blocked, dead}
	Default().Prioritise(in)

	if in[0].WorkerID != "c" || in[1].WorkerID != "b" || in[2].WorkerID != "a" {
		t.Fatalf("order = %s/%s/%s, want c (unreachable), b (unhealthy), a (balance)",
			in[0].WorkerID, in[1].WorkerID, in[2].WorkerID)
	}
}

// Among equally urgent mailboxes the longest-resident goes first: it is the one
// whose placement was decided against the oldest picture of the fleet, and
// ordering by it keeps a busy fleet from starving the same rows every tick.
func TestThePrioritiserBreaksTiesOnResidency(t *testing.T) {
	// Both are past the residency floor, so both are genuinely TierBalance and
	// the tier cannot be what orders them.
	recent := settled("a")
	recent.ResidentFor = Default().ResidencyFloor + time.Hour
	old := settled("b")
	old.ResidentFor = Default().ResidencyFloor + 100*time.Hour

	in := []Incumbent{recent, old}
	Default().Prioritise(in)
	if in[0].WorkerID != "b" {
		t.Fatalf("first = %s, want b (resident longest)", in[0].WorkerID)
	}
}

// Nowhere to go is not an error and not a move. With every live worker refusing
// this provider, rotation reports no move rather than inventing a destination or
// bouncing the mailbox onto another blocked address.
func TestNothingMovesWhenEveryDestinationIsAlsoBlocked(t *testing.T) {
	p := Default()
	inc := settled("a-blocked")
	inc.OKEvents, inc.BlockEvents = 0, 5

	fleet := []fleetscore.Candidate{
		{WorkerID: "a-blocked", ProviderBlockEvents: 5},
		{WorkerID: "b-blocked", ProviderBlockEvents: 2},
	}
	if move, ok := consider(t, p, inc, fleet...); ok {
		t.Fatalf("moved to %s, which the provider is also refusing", move.ToWorkerID)
	}
}

// Self-host, and every deployment with one worker: there is nowhere to rotate
// TO. A no-op, not an error and not a move onto the worker it is already on.
func TestASingleWorkerFleetRotatesNothing(t *testing.T) {
	p := Default()
	inc := settled("a-only")
	inc.OKEvents, inc.BlockEvents = 0, 9 // as urgent as this gets

	if move, ok := consider(t, p, inc, fleetscore.Candidate{WorkerID: "a-only", ProviderBlockEvents: 9}); ok {
		t.Fatalf("moved to %s in a one-worker fleet", move.ToWorkerID)
	}
}

// The incumbent must never be offered as its own destination: "move it to where
// it already is" is a write and a decision-log entry for no change at all.
//
// The liveness flag and the candidate list are two separate reads, so they CAN
// disagree — a worker that missed its window when the scan ran and heartbeated
// before the candidates were measured comes back as an unreachable incumbent
// that is nonetheless in the fleet. The forced tiers pay no margin, so nothing
// else would stop that one.
func TestTheIncumbentIsNeverItsOwnDestination(t *testing.T) {
	p := Default()

	t.Run("best in the fleet", func(t *testing.T) {
		if move, ok := consider(t, p, settled("a-best"), worker("a-best", 0), worker("b-loaded", 30)); ok {
			t.Fatalf("moved to %s; the incumbent is already the best worker", move.ToWorkerID)
		}
	})

	t.Run("reported unreachable but present in the fleet", func(t *testing.T) {
		inc := settled("a-flapping")
		inc.Live = false
		move, ok := consider(t, p, inc, worker("a-flapping", 0), worker("b-loaded", 30))
		if !ok {
			t.Fatal("an unreachable incumbent must still move somewhere")
		}
		if move.ToWorkerID == "a-flapping" {
			t.Fatal("moved to the worker it was already on")
		}
	})
}

// An incumbent the caller reports as live but that is absent from the candidate
// list cannot be scored, and a move decided on a score nobody computed is
// exactly the log entry fleetdecision exists to prevent. Refuse rather than
// guess; the next tick will see a consistent picture.
func TestABalanceMoveIsRefusedWhenTheIncumbentCannotBeScored(t *testing.T) {
	p := Default()
	inc := settled("a-missing")
	if move, ok := consider(t, p, inc, worker("b-empty", 0)); ok {
		t.Fatalf("moved to %s without scoring the incumbent it was compared against", move.ToWorkerID)
	}
}

// The relationships between the numbers are the design; the numbers themselves
// are tuning. Pin the ones the tests above depend on, so a later re-tune that
// breaks the design fails here rather than silently churning a fleet.
func TestPolicyRelationshipsHoldTheDesignInPlace(t *testing.T) {
	p := Default()

	// The margin must be at least the WHOLE headroom range, so the packing term
	// alone — an empty worker against one exactly at target — can never satisfy
	// it. Imbalance on its own is not a reason to move.
	if p.MinScoreMargin < p.Score.HeadroomWeight {
		t.Errorf("MinScoreMargin %v must be at least HeadroomWeight %v, or pure imbalance can move a mailbox",
			p.MinScoreMargin, p.Score.HeadroomWeight)
	}
	// ...and more than any single soft preference can express, so no one term
	// flipping from best to worst justifies discarding IP trust.
	for _, w := range []struct {
		name  string
		value float64
	}{
		{"BlastRadiusWeight", p.Score.BlastRadiusWeight},
		{"ProviderCrowdingWeight", p.Score.ProviderCrowdingWeight},
		{"BandConflictWeight", p.Score.BandConflictWeight},
	} {
		if p.MinScoreMargin <= w.value {
			t.Errorf("MinScoreMargin %v must exceed %s %v", p.MinScoreMargin, w.name, w.value)
		}
	}
	if p.ResidencyFloor <= 0 {
		t.Error("ResidencyFloor must be positive — a zero floor is not a weak brake, it is a deleted one")
	}
	// Strictly greater, not merely at least: a per-destination cap equal to the
	// tick budget lets one worker absorb the entire pass, which is the failure
	// the second cap exists to prevent.
	if p.MaxMovesPerDestination <= 0 || p.MaxMoves <= p.MaxMovesPerDestination {
		t.Errorf("MaxMoves %d must exceed MaxMovesPerDestination %d, which must be positive",
			p.MaxMoves, p.MaxMovesPerDestination)
	}
}
