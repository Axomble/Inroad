package inprocess

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// Fixed ids so every tie-break is decided by the fixture rather than by whatever
// uuid.New() happened to produce: rotation breaks ties on MailboxID, and a test
// whose winner depends on a random draw proves nothing on the run where it passes.
var (
	mailboxA = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	mailboxB = uuid.MustParse("00000000-0000-0000-0000-0000000000b2")
	mailboxC = uuid.MustParse("00000000-0000-0000-0000-0000000000c3")
)

// budgetRow is one pool row for the exposure-budget fixtures: the sending address
// (whose organizational domain IS the fault domain) and the campaign history the
// share is computed over. Capacity is deliberately generous — nothing here is meant
// to be excluded for being capped, so a narrowing can only be the budget's doing.
func budgetRow(id uuid.UUID, email string, weight, dailyCap int32, assigned int64) gen.ListCampaignSenderCandidatesRow {
	r := candidateRow(id, weight, true, mailboxStatusActive, dailyCap, 0)
	r.Email = email
	r.AssignedCount = assigned
	return r
}

// concentratedPool is a two-domain pool in which the OVER-CONCENTRATED domain's
// mailbox wins ModeWeighted by a wide margin: 100x the weight and 100x the capacity
// of the only alternative. An assertion that the alternative was chosen can
// therefore only mean the budget removed the winner — no scoring change could have
// produced it, which is what makes the assertion worth making.
//
// acme.test carries 90 of the campaign's 100 assignments; other.test carries 10.
func concentratedPool() (hot, cool gen.ListCampaignSenderCandidatesRow) {
	return budgetRow(mailboxA, "bulk@acme.test", 100, 1000, 90),
		budgetRow(mailboxB, "solo@other.test", 1, 10, 10)
}

// The proactive half: 90% of the campaign resting on one organizational domain is
// the concentration the budget exists to unwind, and it unwinds it by routing the
// NEXT contact elsewhere rather than by withholding anything.
func TestExposureBudgetRoutesTheNextContactAwayFromTheDominantFaultDomain(t *testing.T) {
	hot, cool := concentratedPool()
	rows := []gen.ListCampaignSenderCandidatesRow{hot, cool}
	eligible := eligibleCandidates(rows, noDomainLanes)
	if len(eligible) != 2 {
		t.Fatalf("eligible = %d rows, want both — the fixture must not gate anything", len(eligible))
	}

	// The fixture proves itself: without the budget the dominant domain wins.
	baseline, err := rotation.Select(rotation.ModeWeighted, eligible)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if baseline.MailboxID != hot.MailboxID.String() {
		t.Fatalf("baseline winner = %s, want the over-concentrated %s — the fixture no longer proves anything",
			baseline.MailboxID, hot.MailboxID)
	}

	budgeted := withinExposureBudget(rows, eligible, noDomainLanes)
	if len(budgeted) != 1 || budgeted[0].MailboxID != cool.MailboxID.String() {
		t.Fatalf("budgeted = %+v, want only %s: acme.test holds 90%% of the campaign", budgeted, cool.MailboxID)
	}
	winner, err := rotation.Select(rotation.ModeWeighted, budgeted)
	if err != nil {
		t.Fatalf("Select on the budgeted set: %v", err)
	}
	if winner.MailboxID != cool.MailboxID.String() {
		t.Errorf("winner = %s, want %s", winner.MailboxID, cool.MailboxID)
	}
}

// The workspace this product is mostly used by has ONE sending domain, where every
// candidate is by definition over budget. The budget must be inert there, not
// fatal: narrowing to nothing would stop mail today to avoid losing mail if
// something failed later.
//
// An over-correction guard — it passes with the wiring absent too, so it is proven
// by MUTATION rather than by revert (see the report).
func TestExposureBudgetLeavesASingleDomainPoolExactlyAsItWas(t *testing.T) {
	rows := []gen.ListCampaignSenderCandidatesRow{
		budgetRow(mailboxA, "one@acme.test", 100, 1000, 90),
		budgetRow(mailboxB, "two@mail.acme.test", 1, 10, 8),
		budgetRow(mailboxC, "three@acme.test", 1, 10, 2),
	}
	eligible := eligibleCandidates(rows, noDomainLanes)
	if len(eligible) != 3 {
		t.Fatalf("eligible = %d rows, want all three", len(eligible))
	}

	budgeted := withinExposureBudget(rows, eligible, noDomainLanes)
	if !reflect.DeepEqual(budgeted, eligible) {
		t.Errorf("budgeted = %+v, want the eligible set unchanged %+v — a single-domain pool has no alternative",
			budgeted, eligible)
	}
}

// A campaign that has assigned nothing yet has no distribution to be lopsided, so
// there is nothing to correct. Guards the degenerate divide-by-total.
func TestExposureBudgetLeavesAFreshCampaignAlone(t *testing.T) {
	rows := []gen.ListCampaignSenderCandidatesRow{
		budgetRow(mailboxA, "one@acme.test", 100, 1000, 0),
		budgetRow(mailboxB, "two@other.test", 1, 10, 0),
	}
	eligible := eligibleCandidates(rows, noDomainLanes)
	budgeted := withinExposureBudget(rows, eligible, noDomainLanes)
	if !reflect.DeepEqual(budgeted, eligible) {
		t.Errorf("budgeted = %+v, want the eligible set unchanged: no assignments, no concentration", budgeted)
	}
}

// A consumer-provider mailbox has no shared fault domain, and the budget must not
// invent one. gmail.com is a registrable domain but not a reputation unit — Google
// does not fail alice@gmail.com because bob@gmail.com did — so two Gmail mailboxes
// must not be throttled as though they shared a fate.
//
// This is the LANE/DOMAIN mismatch made visible: the fault domain and the lane both
// resolve through warmup.SharedReputationDomain, so a consumer mailbox is ungrouped
// on both sides rather than grouped on one and not the other.
//
// The third mailbox is load bearing. Two Gmail mailboxes ALONE would pass whether or
// not the carve-out exists: grouped, they would be 100% of the campaign, over budget,
// unsatisfiable, and handed back by the never-empty fallback — the right answer for
// the wrong reason. A real custom domain alongside them makes the budget satisfiable,
// so dropping the carve-out genuinely changes the outcome.
func TestExposureBudgetDoesNotGroupConsumerMailboxes(t *testing.T) {
	rows := []gen.ListCampaignSenderCandidatesRow{
		budgetRow(mailboxA, "alice@gmail.com", 100, 1000, 45),
		budgetRow(mailboxB, "bob@gmail.com", 1, 10, 45),
		budgetRow(mailboxC, "ops@acme.test", 1, 10, 10),
	}
	eligible := eligibleCandidates(rows, noDomainLanes)
	if len(eligible) != 3 {
		t.Fatalf("eligible = %d rows, want all three", len(eligible))
	}
	// Grouped as one fault domain the two Gmail mailboxes would be 90% of the
	// campaign and both would be dropped, leaving only acme.test.
	budgeted := withinExposureBudget(rows, eligible, noDomainLanes)
	if !reflect.DeepEqual(budgeted, eligible) {
		t.Errorf("budgeted = %+v, want all three: unrelated strangers on gmail.com are not one fault domain",
			budgeted)
	}
}

// laneOn is the workspace's domain-lane fold with a single verdict in it — built the
// same way resolveSender builds it, from a participant address rather than from a
// domain string, so the test cannot key the map by a rule the production fold does
// not use.
func laneOn(email, lane string) warmup.DomainLanes {
	return warmup.WorstLanesByDomain([]warmup.MailboxLane{{Email: email, Lane: lane}})
}

// gradedPool spreads a campaign across THREE domains at shares that sit between the
// lane ceilings: acme.test 25%, beta.test 40%, gamma.test 35%. Every share is under
// the flat 0.6 cap, so nothing here moves unless a LANE ceiling moves it — and
// acme.test's 25% is under watch's 0.35 but over recovery's 0.20, which is what
// makes one fixture able to tell the two lanes apart.
//
// acme.test wins ModeWeighted outright (100x weight, 100x capacity), so "acme still
// wins" and "acme is gone" are unambiguous, and beta.test is the runner-up by id so
// the replacement is deterministic rather than a coin flip between the other two.
func gradedPool() []gen.ListCampaignSenderCandidatesRow {
	return []gen.ListCampaignSenderCandidatesRow{
		budgetRow(mailboxA, "lead@acme.test", 100, 1000, 25),
		budgetRow(mailboxB, "second@beta.test", 1, 10, 40),
		budgetRow(mailboxC, "third@gamma.test", 1, 10, 35),
	}
}

// The reactive half, and the ordering that is not a guess: a domain the evaluator
// has put on WATCH carries less of the campaign than the identical healthy domain,
// and one in RECOVERY carries less still. The same 25% share survives healthy,
// survives watch, and is refused under recovery.
func TestExposureBudgetShrinksADegradingDomainsShareByLane(t *testing.T) {
	rows := gradedPool()
	for _, tc := range []struct {
		name       string
		lanes      warmup.DomainLanes
		wantKept   int
		wantWinner uuid.UUID
	}{
		// The control: nothing is degrading, so 25% is simply under the flat cap.
		{"healthy carries its full share", noDomainLanes, 3, mailboxA},
		// watch's ceiling is 0.35 and the share is 0.25 — still inside it. Load
		// bearing: it proves the recovery case below is the CEILING biting and not
		// merely "any lane at all narrows the set".
		{"watch tolerates a quarter of the campaign", laneOn("ops@acme.test", warmup.LaneWatch), 3, mailboxA},
		// recovery's ceiling is 0.20 and the share is 0.25 — over it. The dominant
		// mailbox leaves the set and the runner-up takes the contact.
		{"recovery does not", laneOn("ops@acme.test", warmup.LaneRecovery), 2, mailboxB},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eligible := eligibleCandidates(rows, tc.lanes)
			if len(eligible) != 3 {
				t.Fatalf("eligible = %d rows, want all three — no lane here withholds new leads", len(eligible))
			}
			budgeted := withinExposureBudget(rows, eligible, tc.lanes)
			if len(budgeted) != tc.wantKept {
				t.Fatalf("budgeted = %+v, want %d candidates", budgeted, tc.wantKept)
			}
			winner, err := rotation.Select(rotation.ModeWeighted, budgeted)
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if winner.MailboxID != tc.wantWinner.String() {
				t.Errorf("winner = %s, want %s", winner.MailboxID, tc.wantWinner)
			}
		})
	}
}

// A QUARANTINED domain is the existing breaker's business and this slice must not
// touch it. Its mailbox never reaches the budget at all — eligibleCandidates removed
// it — so the budget can neither soften the containment nor deepen it.
//
// The failure this guards against is a plausible one: reading "quarantine" as a very
// low ceiling instead of as a removal would put a second implementation of "may this
// send" next to LaneMaySend, and the two would eventually disagree.
//
// An over-correction guard, proven by MUTATION rather than by revert.
func TestQuarantineIsTheGatesDecisionAndTheBudgetLeavesItAlone(t *testing.T) {
	hot, cool := concentratedPool()
	rows := []gen.ListCampaignSenderCandidatesRow{hot, cool}
	lanes := laneOn("ops@acme.test", warmup.LaneQuarantine)

	// The GATE, not the budget, is what removed it.
	eligible := eligibleCandidates(rows, lanes)
	if len(eligible) != 1 || eligible[0].MailboxID != cool.MailboxID.String() {
		t.Fatalf("eligible = %+v, want only %s: the quarantined domain is withheld before any budget runs",
			eligible, cool.MailboxID)
	}
	if budgeted := withinExposureBudget(rows, eligible, lanes); !reflect.DeepEqual(budgeted, eligible) {
		t.Errorf("budgeted = %+v, want the gate's set unchanged %+v", budgeted, eligible)
	}
	// And the ceiling itself holds no opinion about a lane the breaker owns, so a
	// quarantined domain that DID reach the budget would get the flat cap rather
	// than a refusal.
	if got := warmup.ExposureCeiling(warmup.LaneQuarantine); got != 0 {
		t.Errorf("ExposureCeiling(quarantine) = %v, want 0 — containment is LaneMaySend's decision, not a ceiling", got)
	}
}
