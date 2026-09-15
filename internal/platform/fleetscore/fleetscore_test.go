package fleetscore

import (
	"math"
	"testing"
)

// smtpWorker builds a candidate carrying n SMTP mailboxes that belong to OTHER
// workspaces and share the incoming mailbox's band — the neutral fixture every
// test below perturbs in exactly one dimension, so a changed verdict can only
// come from the dimension under test. Tenant ownership is left at zero on
// purpose: with no candidate holding any of this workspace's mailboxes, the
// blast-radius term is identical everywhere and cannot quietly decide a test
// that is about something else.
func smtpWorker(id string, n int) Candidate {
	return Candidate{WorkerID: id, SMTPMailboxes: n}
}

// winner runs the policy over candidates and returns the top-ranked worker id,
// failing the test if nothing was eligible (every caller here expects a pick).
func winner(t *testing.T, p Policy, in Incoming, candidates ...Candidate) string {
	t.Helper()
	ranked := p.Rank(candidates, in)
	if len(ranked) == 0 {
		t.Fatalf("no eligible candidate among %d", len(candidates))
	}
	return ranked[0].WorkerID
}

// A worker with more spare capacity wins, all else equal. This is the packing
// term, and it is the only reason the pre-scoring assigner ever had for
// preferring one worker over another.
func TestRankPrefersTheWorkerWithMoreHeadroom(t *testing.T) {
	p := Default()
	// The incoming mailbox is gmail and both workers carry only SMTP, so the
	// provider-crowding term is identical on both and load is the ONLY
	// difference. "a-heavy" sorts FIRST alphabetically, so with a zero headroom
	// weight the deterministic tie-break would hand it the win.
	got := winner(t, p, Incoming{Provider: "gmail"}, smtpWorker("a-heavy", 12), smtpWorker("b-light", 3))
	if got != "b-light" {
		t.Fatalf("winner = %q, want b-light — the lighter worker has more headroom", got)
	}
}

// Capacity is a TARGET, not a ceiling. A worker past its target must score
// badly enough to lose to any worker still under it, and must still be
// eligible — refusing to place on a full fleet refuses exactly when refusing
// hurts most.
func TestOverTargetWorkerLosesToAnUnderTargetOneButStaysEligible(t *testing.T) {
	p := Default()
	over := smtpWorker("a-over", int(p.TargetLoad)*2)
	under := smtpWorker("b-under", int(p.TargetLoad)-1)

	if got := winner(t, p, Incoming{Provider: "smtp"}, over, under); got != "b-under" {
		t.Fatalf("winner = %q, want b-under — an over-target worker must lose to one under target", got)
	}
	// Alone, the over-target worker is still a legal placement.
	ranked := p.Rank([]Candidate{over}, Incoming{Provider: "smtp"})
	if len(ranked) != 1 || ranked[0].WorkerID != "a-over" {
		t.Fatalf("an over-target worker must stay eligible when it is all there is, got %+v", ranked)
	}
}

// The one remedy that must always work: adding a worker to a saturated fleet.
// If youth were ever a capacity multiplier, a brand-new worker would score as
// overloaded after its first mailbox and scaling out would relieve nothing.
func TestAFreshWorkerWinsAgainstASaturatedFleet(t *testing.T) {
	p := Default()
	saturated := []Candidate{
		smtpWorker("a-full", int(p.TargetLoad)*2),
		smtpWorker("b-full", int(p.TargetLoad)*2),
		{WorkerID: "z-fresh"},
	}
	if got := winner(t, p, Incoming{Provider: "smtp"}, saturated...); got != "z-fresh" {
		t.Fatalf("winner = %q, want z-fresh — adding a worker must relieve a full fleet", got)
	}
}

// Blast radius: concentrating one tenant's mailboxes on one egress IP means one
// bad IP takes that tenant off the air entirely. It outranks raw packing, so a
// worker carrying MORE total load but none of this tenant wins.
func TestBlastRadiusSpreadsOneTenantAcrossWorkers(t *testing.T) {
	p := Default()
	// Identical in every other dimension — same count, same provider, same
	// band — so load, crowding and band contribute exactly the same to both.
	// The only difference is WHOSE mailboxes they are. "a-mine" sorts first, so
	// a zero blast-radius weight would hand it the win on the tie-break.
	mine := Candidate{WorkerID: "a-mine", SMTPMailboxes: 10, SameWorkspaceMailboxes: 10}
	theirs := Candidate{WorkerID: "b-theirs", SMTPMailboxes: 10, SameWorkspaceMailboxes: 0}

	if got := winner(t, p, Incoming{Provider: "smtp"}, mine, theirs); got != "b-theirs" {
		t.Fatalf("winner = %q, want b-theirs — a tenant must not be concentrated on one worker", got)
	}
}

// Provider crowding is what the per-worker provider signals exist to inform: a
// provider rate-limits and challenges per source IP, so 15 Gmail mailboxes
// authenticating from one address is the shape that earns a 421 4.7.28.
// Equal load, different provider mix — the incoming mailbox's own provider
// decides.
func TestProviderCrowdingPrefersTheWorkerCarryingFewerOfTheSameProvider(t *testing.T) {
	p := Default()
	// Identical weighted load (both API mailboxes) and identical tenant share:
	// the ONLY difference is which provider those mailboxes authenticate to.
	// "a-gmail" sorts first, so a zero crowding weight would hand it the win on
	// the tie-break.
	crowded := Candidate{WorkerID: "a-gmail", GmailMailboxes: 15}
	roomy := Candidate{WorkerID: "b-m365", M365Mailboxes: 15}

	if got := winner(t, p, Incoming{Provider: "gmail"}, crowded, roomy); got != "b-m365" {
		t.Fatalf("winner = %q, want b-m365 — a gmail mailbox must avoid the worker already authenticating 15 of them", got)
	}
}

// The risk band is a WEAK term: it breaks a tie and never overrides load. The
// recipient never sees a worker's egress IP, so recipient-derived reputation
// cannot transfer between mailboxes sharing one — segregating on it is a
// preference, not a partition.
func TestBandBreaksATieButNeverOverridesLoad(t *testing.T) {
	p := Default()

	// Equal in every other dimension: the band decides. "a-otherband" sorts
	// first, so a zero band weight would pick it.
	sameLoadOther := Candidate{WorkerID: "a-otherband", SMTPMailboxes: 5, OtherBandMailboxes: 5}
	sameLoadSame := Candidate{WorkerID: "b-sameband", SMTPMailboxes: 5}
	if got := winner(t, p, Incoming{Provider: "smtp"}, sameLoadOther, sameLoadSame); got != "b-sameband" {
		t.Fatalf("tie-break winner = %q, want b-sameband — the band decides when nothing else does", got)
	}

	// Now give the same-band worker a materially heavier load. Load wins: a
	// band preference strong enough to pile mailboxes onto a loaded worker would
	// be the partition this design replaced.
	heavySame := Candidate{WorkerID: "a-sameband-heavy", SMTPMailboxes: 20}
	lightOther := Candidate{WorkerID: "b-otherband-light", SMTPMailboxes: 2, OtherBandMailboxes: 2}
	if got := winner(t, p, Incoming{Provider: "smtp"}, heavySame, lightOther); got != "b-otherband-light" {
		t.Fatalf("loaded-vs-light winner = %q, want b-otherband-light — the band must not outweigh load", got)
	}
}

// An API-backed mailbox (Gmail/Graph: HTTPS, no persistent IMAP connection)
// costs a worker far less than an SMTP+IMAP one, so a flat per-mailbox count
// misprices a worker's real occupancy.
func TestAPIMailboxesCostLessThanSMTPMailboxes(t *testing.T) {
	p := Default()
	// Same mailbox COUNT on both, so a count-based scorer sees a tie and the
	// alphabetical tie-break picks "a-smtp". The incoming mailbox is m365,
	// which NEITHER worker already carries, so provider crowding is identical
	// and only the per-mailbox price can separate them.
	smtp := Candidate{WorkerID: "a-smtp", SMTPMailboxes: 10}
	api := Candidate{WorkerID: "b-api", GmailMailboxes: 10}

	if got := winner(t, p, Incoming{Provider: "m365"}, smtp, api); got != "b-api" {
		t.Fatalf("winner = %q, want b-api — ten API mailboxes occupy a worker less than ten SMTP ones", got)
	}
	if p.APIMailboxWeight >= p.SMTPMailboxWeight {
		t.Fatalf("APIMailboxWeight %v must be below SMTPMailboxWeight %v", p.APIMailboxWeight, p.SMTPMailboxWeight)
	}
}

// Health is the ONLY hard gate. A worker whose entire recent conversation with
// this provider was refusals to talk to it cannot serve a new mailbox of that
// provider, however much room it has.
func TestAWorkerTheProviderOnlyRefusesIsIneligible(t *testing.T) {
	p := Default()
	blocked := Candidate{WorkerID: "a-blocked", ProviderBlockEvents: 3}
	healthy := smtpWorker("b-healthy", int(p.TargetLoad)*2)

	ranked := p.Rank([]Candidate{blocked, healthy}, Incoming{Provider: "smtp"})
	if len(ranked) != 1 {
		t.Fatalf("ranked %d candidates, want 1 — the blocked worker must be excluded: %+v", len(ranked), ranked)
	}
	if ranked[0].WorkerID != "b-healthy" {
		t.Fatalf("eligible winner = %q, want b-healthy", ranked[0].WorkerID)
	}
}

// ...but a worker that is being blocked AND still succeeding is degraded, not
// dead. Excluding it on the first refusal would take a whole fleet out of
// service on one provider's bad afternoon, which is the failure mode a hard
// capacity ceiling has and this gate must not repeat.
func TestAWorkerStillSucceedingStaysEligibleDespiteBlocks(t *testing.T) {
	p := Default()
	degraded := Candidate{WorkerID: "a-degraded", ProviderBlockEvents: 3, ProviderOKEvents: 40}

	ranked := p.Rank([]Candidate{degraded}, Incoming{Provider: "smtp"})
	if len(ranked) != 1 || ranked[0].WorkerID != "a-degraded" {
		t.Fatalf("a worker still completing operations must stay eligible, got %+v", ranked)
	}
}

// A worker nothing has been recorded about is NEW, not unhealthy. Reading an
// empty signal window as a block would make a freshly added worker ineligible —
// again defeating the one remedy for a saturated fleet.
func TestAWorkerWithNoSignalsAtAllIsEligible(t *testing.T) {
	ranked := Default().Rank([]Candidate{{WorkerID: "fresh"}}, Incoming{Provider: "gmail"})
	if len(ranked) != 1 {
		t.Fatalf("a worker with no recorded signals must be eligible, got %+v", ranked)
	}
}

// Refusal is the honest answer when the gate excludes everything, and it is the
// ONLY thing that produces one.
func TestRankReturnsNothingWhenEveryCandidateIsBlocked(t *testing.T) {
	p := Default()
	ranked := p.Rank([]Candidate{
		{WorkerID: "a", ProviderBlockEvents: 1},
		{WorkerID: "b", ProviderBlockEvents: 9},
	}, Incoming{Provider: "smtp"})
	if len(ranked) != 0 {
		t.Fatalf("ranked %+v, want nothing eligible", ranked)
	}
}

// Two callers placing the same mailbox against the same fleet state must reach
// the same answer, or a mailbox's mail splits across two egress IPs — the exact
// failure the per-worker pin exists to prevent. Ties therefore resolve on
// worker_id, the same deterministic rule every pre-scoring pick used.
func TestTiesResolveDeterministicallyOnWorkerID(t *testing.T) {
	p := Default()
	in := Incoming{Provider: "smtp"}
	first := p.Rank([]Candidate{smtpWorker("m", 4), smtpWorker("a", 4), smtpWorker("z", 4)}, in)
	second := p.Rank([]Candidate{smtpWorker("z", 4), smtpWorker("m", 4), smtpWorker("a", 4)}, in)

	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("expected 3 eligible each, got %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i].WorkerID != second[i].WorkerID {
			t.Fatalf("ranking depends on input order: %v vs %v", ids(first), ids(second))
		}
	}
	if first[0].WorkerID != "a" {
		t.Fatalf("tie-break winner = %q, want a (lowest worker_id)", first[0].WorkerID)
	}
}

func ids(r []Ranked) []string {
	out := make([]string, 0, len(r))
	for _, c := range r {
		out = append(out, c.WorkerID)
	}
	return out
}

// The weight relationships are the design, not the individual numbers, and they
// are what a later re-tune will break silently. Pin the two orderings the rest
// of this file depends on:
//
//   - the over-target ramp must dominate every other penalty COMBINED, so no
//     mix of tenant, provider and band preferences can talk placement into
//     stacking onto a worker that is already past its target;
//   - the band must be the weakest term there is, because it is the one whose
//     causal story (recipient-side reputation) does not reach a worker's egress
//     IP at all.
func TestWeightOrderingHoldsTheDesignInPlace(t *testing.T) {
	p := Default()
	softPenalties := p.BlastRadiusWeight + p.ProviderCrowdingWeight + p.BandConflictWeight
	if p.OverloadWeight <= softPenalties+p.HeadroomWeight {
		t.Errorf("OverloadWeight %v must exceed every other term combined (%v), or an over-target worker can win",
			p.OverloadWeight, softPenalties+p.HeadroomWeight)
	}
	if p.BandConflictWeight >= p.ProviderCrowdingWeight || p.BandConflictWeight >= p.BlastRadiusWeight {
		t.Errorf("BandConflictWeight %v must be the weakest term (crowding %v, blast %v)",
			p.BandConflictWeight, p.ProviderCrowdingWeight, p.BlastRadiusWeight)
	}
	if p.BandConflictWeight <= 0 {
		t.Error("BandConflictWeight must be positive — a zero weight is not a weak preference, it is a deleted one")
	}
}

// Score must stay a finite number for every input a caller can construct. A NaN
// or an Inf from a zero denominator would make sort order undefined and the
// logged score meaningless.
func TestScoreIsFiniteForDegenerateInputs(t *testing.T) {
	p := Default()
	for _, tc := range []struct {
		name string
		c    Candidate
		in   Incoming
	}{
		{"empty worker, empty provider", Candidate{WorkerID: "w"}, Incoming{}},
		{"unknown provider", Candidate{WorkerID: "w", SMTPMailboxes: 3}, Incoming{Provider: "carrier-pigeon"}},
		{"negative counts", Candidate{WorkerID: "w", SMTPMailboxes: -5, SameWorkspaceMailboxes: -5}, Incoming{Provider: "smtp"}},
	} {
		got := p.score(tc.c, tc.in, 0)
		if math.IsNaN(got) || math.IsInf(got, 0) {
			t.Errorf("%s: score = %v, want a finite number", tc.name, got)
		}
	}
}

// MailboxWeight is the per-mailbox cost the whole load model rests on. An
// unknown provider string must price as the EXPENSIVE leg, because that is what
// mail.MultiSender actually dials for anything it does not recognise — pricing
// it as cheap would under-count a real SMTP mailbox.
func TestUnknownProviderIsPricedAsSMTP(t *testing.T) {
	p := Default()
	if got := p.MailboxWeight(""); got != p.SMTPMailboxWeight {
		t.Errorf("MailboxWeight(\"\") = %v, want the SMTP weight %v", got, p.SMTPMailboxWeight)
	}
	if got := p.MailboxWeight("carrier-pigeon"); got != p.SMTPMailboxWeight {
		t.Errorf("MailboxWeight(unknown) = %v, want the SMTP weight %v", got, p.SMTPMailboxWeight)
	}
	if got := p.MailboxWeight("m365"); got != p.APIMailboxWeight {
		t.Errorf("MailboxWeight(\"m365\") = %v, want the API weight %v", got, p.APIMailboxWeight)
	}
}
