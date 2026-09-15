// Package fleetrotate decides whether an ALREADY PLACED mailbox should move to
// a different worker, and where to.
//
// It is the separate urgency gate fleetscore deliberately does not contain.
// Placement (internal/platform/fleetscore, and its caller
// coreapi/inprocess.AssignMailboxWorker) answers "where should this mailbox go?"
// and treats a live incumbent as unconditional — a fresh placement is only ever
// consulted for a mailbox that has no home. That leaves exactly one hole, and
// this package exists to close it: a worker whose IP the provider has blocked
// keeps every mailbox already assigned to it, and each of them keeps failing,
// forever, because nothing ever re-asks the question.
//
// # THE PRINCIPLE THIS PACKAGE IS FIGHTING, AND MUST KEEP FIGHTING
//
// Incumbency is the heaviest force in placement ON PURPOSE. IP trust accrues per
// (mailbox, IP) pair at the PROVIDER — it is what decides whether a sign-in from
// that address is challenged and how hard it is throttled — and moving a mailbox
// discards it. Rotation is the deliberate exception to that rule and it has to
// stay an exception: a gate that fires easily undoes the stability placement
// exists to provide, and a fleet that churns away weeks of accrued reputation is
// strictly worse off than one that never rotates at all.
//
// So every number here is a BRAKE, not a preference, and each is set to make
// moving hard rather than to make the fleet tidy:
//
//   - TIERS. An unhealthy or unreachable worker justifies an immediate move;
//     mere imbalance justifies almost nothing. Tier is the whole of that
//     distinction and it is what the residency floor and the margin are keyed
//     on.
//   - A RESIDENCY FLOOR, which only the opportunistic tier pays. A mailbox that
//     has just arrived somewhere has nothing worth weighing against a move.
//   - A MARGIN the destination must BEAT, never tie. A destination that scores
//     trivially better is not a reason to discard IP trust.
//   - CAPS, per tick and per destination. See Policy.MaxMovesPerDestination for
//     why the second one matters more than it looks.
//
// HEALTH HAS ONE DEFINITION AND IT IS NOT DEFINED HERE.
// fleetscore.Candidate.Eligible already says what "this provider will not talk
// to this worker" means, and placement gates on it. Rotation reuses that exact
// predicate (see Policy.Tier) rather than introducing a second notion of health:
// two disagreeing definitions would be worse than either alone, because a worker
// could then be too sick to receive new mailboxes and well enough to keep the
// ones it has — which is precisely today's bug with the sign flipped.
//
// Nothing here does I/O or knows what a database is. A decision is a pure
// function of measured facts, so the policy is unit-testable without Postgres
// and the orchestration (coreapi/inprocess) holds no policy.
package fleetrotate

import (
	"fmt"
	"slices"
	"time"

	"github.com/inroad/inroad/internal/platform/fleetdecision"
	"github.com/inroad/inroad/internal/platform/fleetscore"
)

// Tier is how badly a mailbox needs to move. The ORDER is meaningful: a larger
// tier is more urgent, which is what lets Prioritise sort by it and what decides
// who gets the move budget when it binds.
type Tier int

const (
	// TierNone: leave it alone. Either nothing is wrong, or the only thing that
	// could be improved is not worth re-deciding yet (see Policy.Tier).
	TierNone Tier = iota
	// TierBalance is the opportunistic tier: nothing is WRONG with the
	// incumbent, some other worker is merely a better home. It pays the
	// residency floor and the margin, and it is the tier that must almost never
	// fire.
	TierBalance
	// TierUnhealthy: the provider has been refusing this worker for this
	// mailbox's provider and has completed nothing since — fleetscore.Eligible's
	// own definition. The mailbox's sends are failing where it is.
	TierUnhealthy
	// TierUnreachable: the worker stopped heartbeating. Its affinity queue has
	// no consumer, so the mailbox's tasks neither run nor fail nor alert. Ranked
	// above TierUnhealthy because a blocked worker is still executing (it can
	// report, retry and recover) while an absent one is silence.
	TierUnreachable
)

// Urgent reports whether this tier may bypass the residency floor and the score
// margin. Both brakes exist to stop a mailbox moving for a rounding difference;
// neither has anything to say about a mailbox that cannot send where it is.
func (t Tier) Urgent() bool { return t >= TierUnhealthy }

// String names the tier for logs and metrics labels.
func (t Tier) String() string {
	switch t {
	case TierBalance:
		return "balance"
	case TierUnhealthy:
		return "unhealthy"
	case TierUnreachable:
		return "unreachable"
	default:
		return "none"
	}
}

// Incumbent is one already-placed mailbox and the measured facts about the
// worker it currently sits on. Every field is something the caller MEASURED;
// nothing here is derived, so a decision made from it can be reproduced.
type Incumbent struct {
	MailboxID   string
	WorkspaceID string
	// Provider is the mailbox's transport leg ("smtp" | "gmail" | "m365"),
	// which decides both what it costs a worker and which of the incumbent's
	// provider signals below are the relevant ones.
	Provider string
	// Band is the risk band this mailbox is being rotated UNDER — its CURRENT
	// band, not necessarily the one on the assignment row. A rotation is a fresh
	// placement decision, so it is scored and recorded under the band that is
	// true now.
	Band string
	// WorkerID is where the mailbox sits today: the thing a move is leaving.
	WorkerID string
	// ResidentFor is how long it has sat there. The residency floor is measured
	// against this, and it is a DURATION rather than a timestamp so this package
	// needs no clock and a test needs no fake one.
	ResidentFor time.Duration
	// Live is whether that worker heartbeated inside the fleet's live window. A
	// worker outside it does not appear in the candidate list at all, which is
	// why unreachability cannot be derived from the provider signals below.
	Live bool

	// OKEvents / BlockEvents are the incumbent worker's recent verdicts FROM
	// THIS MAILBOX'S PROVIDER, over the same window and with the same
	// classification placement reads (ListPlacementCandidates). They exist to be
	// handed to fleetscore.Eligible unchanged — see Policy.Tier.
	OKEvents    int64
	BlockEvents int64
}

// health is the incumbent's own worker expressed as the type the ONE definition
// of fleet health is written against, so Tier can ask fleetscore.Eligible rather
// than restate it. Only the two signal fields are populated: Eligible reads
// nothing else, and filling in load counts that no caller measured for this
// purpose would invite a future reader to trust them.
func (i Incumbent) health() fleetscore.Candidate {
	return fleetscore.Candidate{
		WorkerID:            i.WorkerID,
		ProviderOKEvents:    i.OKEvents,
		ProviderBlockEvents: i.BlockEvents,
	}
}

// Move is one decided rotation, ready to persist and to log. It carries the
// finished Reason rather than the ingredients for one: fleetdecision.Reason can
// only be built through its constructors, so deciding the reason HERE — where
// the tier is known — is what makes "a forced move never prints a comparison it
// did not make" a property of the decision rather than of the call site.
type Move struct {
	MailboxID    string
	WorkspaceID  string
	FromWorkerID string
	ToWorkerID   string
	// Band is the band the rotated assignment is recorded under, carried through
	// from the Incumbent so the row matches the decision that produced it.
	Band   string
	Tier   Tier
	Reason fleetdecision.Reason
}

// Policy holds the brakes. A value rather than package constants, for
// fleetscore.Policy's reasons: a test can vary one number, the relationships
// BETWEEN them (which are the actual design) can be asserted, and a composition
// root could one day make them configurable.
type Policy struct {
	// Score is the placement policy rotation compares destinations with. It is
	// the SAME scorer placement uses, deliberately: rotation asks "if this
	// mailbox were being placed right now, where would it go?", and a rotation
	// that preferred workers placement would not would fight the placer forever.
	Score fleetscore.Policy

	// ResidencyFloor is the minimum time on a worker before a NON-URGENT
	// rotation is even considered. Urgent tiers bypass it (Tier.Urgent).
	//
	// 12 hours, and the derivation matters more than the number. The evidence an
	// opportunistic move is decided on is at most one hour old — that is
	// inprocess.providerSignalWindow, the width of the health window placement
	// and rotation both read, and occupancy is instantaneous. A floor shorter
	// than that window would let a mailbox be moved on evidence generated while
	// it sat somewhere else. Twelve hours is twelve consecutive windows of
	// evidence about THIS placement before the fleet is allowed to second-guess
	// it for a reason that is not urgent, and it bounds opportunistic churn at
	// two egress-IP changes per mailbox per day against a provider that forms
	// its opinion of an address over days. It is not the only brake: the margin
	// and the caps below are what make the common case zero moves, and the floor
	// is what makes them unnecessary in the first place.
	ResidencyFloor time.Duration

	// MinScoreMargin is how much better a destination must score than the
	// incumbent before an opportunistic move is allowed. STRICTLY better by this
	// much — a tie, or anything under it, leaves the mailbox where it is.
	//
	// 10, which is exactly Score.HeadroomWeight, and that identity is the point:
	// the packing term spans [0, HeadroomWeight] across its entire range, so an
	// empty worker against one exactly at target differs by 10 and the strict
	// comparison refuses it. IMBALANCE ALONE THEREFORE CANNOT MOVE A MAILBOX. An
	// opportunistic move needs something imbalance is not — an incumbent past
	// TargetLoad, where OverloadWeight (60) ramps, or several soft penalties
	// compounding — which is the only imbalance that is costing the fleet
	// anything. It also exceeds every soft penalty individually (blast radius 8,
	// provider crowding 6, band 2), so no single preference flipping from best
	// to worst can talk rotation into discarding IP trust
	// (TestPolicyRelationshipsHoldTheDesignInPlace).
	MinScoreMargin float64

	// MaxMoves is the fleet-wide budget for ONE tick: the most of the fleet's
	// accumulated IP history a single pass may discard.
	//
	// 20 = 4 × MaxMovesPerDestination, so a tick that spends its whole budget
	// has spread it over at least four destinations; on a fleet smaller than
	// that the per-destination cap binds first and the tick simply moves fewer,
	// which is the safe direction. Nothing is lost by stopping early — the next
	// tick re-reads the fleet and the urgent rows sort to the front again, so a
	// worker with hundreds of blocked mailboxes drains over several ticks
	// instead of arriving somewhere else all at once.
	MaxMoves int

	// MaxMovesPerDestination is the most one worker may ABSORB in a tick, and it
	// matters more than it looks.
	//
	// Destination scores are measured, then acted on. Between the measurement
	// and the write, the send path is placing mailboxes this tick cannot see, so
	// the emptiest worker in the picture is a worker several callers may be
	// converging on at once. Without this cap one pass can fill it.
	//
	// 5 is one eighth of Score.TargetLoad (40): the most one tick may add to a
	// worker while the reading it was chosen on stays approximately true. The
	// cap is a bound on a bad reading, not a load limit — the scorer's
	// OverloadWeight is what prices load.
	MaxMovesPerDestination int
}

// Default is the tuned policy the rotation sweep uses. Every number is justified
// on its field; the RELATIONSHIPS between them are the design, and
// TestPolicyRelationshipsHoldTheDesignInPlace pins those.
func Default() Policy {
	return Policy{
		Score:                  fleetscore.Default(),
		ResidencyFloor:         12 * time.Hour,
		MinScoreMargin:         10,
		MaxMoves:               20,
		MaxMovesPerDestination: 5,
	}
}

// Tier classifies how badly one mailbox needs to move, from the incumbent alone
// — no destination is consulted, so a caller can sort a whole tick's worth of
// candidates before spending a single query on scoring them.
//
// The residency floor is expressed HERE, as TierNone, rather than as a separate
// check inside Consider: a mailbox that has not settled is not a weak rotation
// candidate, it is not a candidate at all, and folding it into the tier is what
// stops Prioritise from ranking it above one that is.
func (p Policy) Tier(inc Incumbent) Tier {
	switch {
	case !inc.Live:
		return TierUnreachable
	case !inc.health().Eligible():
		// fleetscore's definition, unchanged: the provider has refused this
		// worker recently and it has completed nothing since. A worker with no
		// signals at all is NEW, not unhealthy, and Eligible already says so.
		return TierUnhealthy
	case inc.ResidentFor < p.ResidencyFloor:
		return TierNone
	default:
		return TierBalance
	}
}

// Prioritise orders a tick's candidates so the move budget is spent on the
// mailboxes that are actually broken: most urgent first, and among equally
// urgent ones the longest-resident first.
//
// Residency is the tie-break because it is the fairest one available: the
// longest-resident placement was decided against the oldest picture of the
// fleet, and a mailbox that moves has its residency reset, so it goes to the
// back and cannot monopolise successive ticks.
//
// Sorted in place, and stably, so two candidates equal on both keys keep the
// order the caller's query produced (which is itself deterministic) rather than
// depending on the sort's internals.
func (p Policy) Prioritise(candidates []Incumbent) {
	slices.SortStableFunc(candidates, func(a, b Incumbent) int {
		if d := p.Tier(b) - p.Tier(a); d != 0 {
			return int(d)
		}
		return int(b.ResidentFor - a.ResidentFor)
	})
}

// Plan is one tick's rotation decisions in progress. It exists only to carry the
// two caps across the mailboxes considered in that tick; it holds no other
// state, and a new one starts every tick.
type Plan struct {
	policy Policy
	moved  int
	byDest map[string]int
}

// NewPlan starts a tick.
func (p Policy) NewPlan() *Plan {
	return &Plan{policy: p, byDest: map[string]int{}}
}

// Full reports whether the tick's move budget is spent, so a caller can stop
// scanning rather than keep scoring mailboxes no budget remains for.
func (pl *Plan) Full() bool { return pl.moved >= pl.policy.MaxMoves }

// Consider decides whether this mailbox moves, given the live workers measured
// for it. fleet is the same measurement placement would make for this mailbox —
// every live worker, with counts parameterised by the mailbox's own workspace,
// band and provider — so "where would this mailbox be placed today?" and "is
// that better than where it is?" are answered by one scorer, not two.
//
// It reports (Move, true) only when the mailbox should move. A move it returns
// is COUNTED against both caps immediately, before the caller has written
// anything: a caller whose write then finds the mailbox already re-placed
// elsewhere has spent a unit of budget on nothing, which is the conservative
// direction and the only one that keeps the caps honest when a write fails.
func (pl *Plan) Consider(inc Incumbent, fleet []fleetscore.Candidate) (Move, bool) {
	tier := pl.policy.Tier(inc)
	if tier == TierNone || pl.Full() {
		return Move{}, false
	}

	// Score the fleet as it would be if this mailbox were NOT where it is. That
	// counterfactual is what makes staying and moving comparable: every
	// destination is scored with the mailbox arriving (fleetscore adds the
	// incoming mailbox itself), so the incumbent has to be scored the same way,
	// which means taking the mailbox off it first. Without this the incumbent is
	// charged for the mailbox twice and every comparison leans toward moving.
	ranked := pl.policy.Score.Rank(vacated(fleet, inc), fleetscore.Incoming{Provider: inc.Provider})

	best := -1
	incumbentScore, incumbentScored := 0.0, false
	for i, r := range ranked {
		if r.WorkerID == inc.WorkerID {
			// The incumbent is the thing being compared AGAINST, never a
			// destination: "move it to where it already is" is a write and a log
			// entry for no change at all.
			incumbentScore, incumbentScored = r.Score, true
			continue
		}
		if best < 0 && pl.byDest[r.WorkerID] < pl.policy.MaxMovesPerDestination {
			best = i
		}
	}
	if best < 0 {
		// Nowhere to go: every other live worker is refusing this provider (Rank
		// dropped them), has taken its share of this tick, or there is no other
		// live worker at all — the self-host case, where rotation is a no-op by
		// arithmetic rather than by a special branch.
		return Move{}, false
	}

	reason, ok := pl.policy.reason(tier, inc, ranked[best], incumbentScore, incumbentScored, len(fleet))
	if !ok {
		return Move{}, false
	}

	pl.moved++
	pl.byDest[ranked[best].WorkerID]++
	return Move{
		MailboxID:    inc.MailboxID,
		WorkspaceID:  inc.WorkspaceID,
		FromWorkerID: inc.WorkerID,
		ToWorkerID:   ranked[best].WorkerID,
		Band:         inc.Band,
		Tier:         tier,
		Reason:       reason,
	}, true
}

// reason applies the margin and renders the explanation, and returns ok=false
// for a comparison that does not clear it. The two live together because they
// are the same question asked twice: an opportunistic move is allowed exactly
// when there is a real comparison to report, and a forced one is allowed exactly
// when there is none to make.
func (p Policy) reason(tier Tier, inc Incumbent, dest fleetscore.Ranked, incumbentScore float64, incumbentScored bool, considered int) (fleetdecision.Reason, bool) {
	if tier.Urgent() {
		return fleetdecision.Forced(p.forcedCause(tier, inc, dest.WorkerID)), true
	}
	if !incumbentScored {
		// The caller reported a live incumbent that is not in the fleet it
		// measured — a picture that changed under the query. There is no
		// incumbent score, so there is no comparison, and an opportunistic move
		// justified by a number nobody computed is the exact log entry
		// fleetdecision exists to prevent. Refuse; the next tick sees a
		// consistent fleet.
		return fleetdecision.Reason{}, false
	}
	if dest.Score <= incumbentScore+p.MinScoreMargin {
		return fleetdecision.Reason{}, false
	}
	return fleetdecision.Chose(
		fleetdecision.Candidate{WorkerID: dest.WorkerID, Score: dest.Score},
		fleetdecision.Candidate{WorkerID: inc.WorkerID, Score: incumbentScore},
		considered,
	), true
}

// forcedCause is the operator-actionable prose for a move that involved no
// comparison. It says what the incumbent did, never what the destination scored:
// the destination was the best of what remained, but nothing was weighed against
// it, and naming a number here would invite an operator to tune a threshold that
// decided nothing.
func (p Policy) forcedCause(tier Tier, inc Incumbent, dest string) string {
	switch tier {
	case TierUnreachable:
		return fmt.Sprintf(
			"worker %s stopped heartbeating, so its affinity queue has no consumer and this mailbox's tasks were neither running nor failing; it was moved to %s",
			inc.WorkerID, dest)
	default:
		return fmt.Sprintf(
			"provider %q has been refusing worker %s (blocked or unreachable) throughout the health window with no completed operation since, so this mailbox could not send from it; it was moved to %s",
			inc.Provider, inc.WorkerID, dest)
	}
}

// vacated is the fleet as it would be with this mailbox taken off its incumbent
// worker. The incumbent's own row is the only one that changes; a copy is
// returned because the caller's slice is a measurement and rotation must not
// rewrite it.
func vacated(fleet []fleetscore.Candidate, inc Incumbent) []fleetscore.Candidate {
	out := slices.Clone(fleet)
	for i := range out {
		if out[i].WorkerID == inc.WorkerID {
			out[i] = out[i].Vacating(inc.Provider)
		}
	}
	return out
}
