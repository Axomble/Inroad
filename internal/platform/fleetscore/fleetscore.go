// Package fleetscore ranks live workers for a mailbox placement.
//
// It replaces a tiered FILTER (pure-band match, then idle promotion, then
// already-mixed last resort, then refuse) with an additive SCORE over eligible
// workers, because filters compose multiplicatively and scores compose
// additively. Every new constraint added to a filter chain is another gate, and
// conjoined gates eventually admit nothing — so the fleet refuses placements
// exactly when it is fullest, which is when refusing is worst. A term added to a
// sum can only shift a preference.
//
// Three rules shape everything here:
//
//  1. HEALTH IS THE ONLY HARD GATE. A capacity number is a guess about a
//     provider's real limits; refusing on a guess refuses when you are busiest.
//     Exceeding the target produces a heavily degraded score, never
//     ineligibility (TestOverTargetWorkerLosesToAnUnderTargetOneButStaysEligible).
//  2. INCUMBENCY OUTRANKS PACKING. It is not a weight in this package, because
//     it is not a preference: IP trust accrues per (mailbox, IP) pair at the
//     PROVIDER and moving a mailbox discards it, so a mailbox already on a live
//     worker stays there and never reaches this package at all. See
//     coreapi/inprocess.AssignMailboxWorker's step 1 — expressed as a weight the
//     rule would be an arbitrarily large number nothing could outvote; expressed
//     as control flow it is an early return that also saves the fleet scan.
//  3. THE RISK BAND IS A WEAK TERM, NOT A PARTITION. A mailbox's warmup lane is
//     derived entirely from RECIPIENT-side signals (bounces, complaints, spam
//     placement), and Inroad never delivers to a recipient's MX: every send
//     authenticates to the customer's own provider, which delivers from its own
//     outbound pool. The recipient never observes a worker's egress IP, so
//     recipient-side reputation cannot transfer between mailboxes sharing a
//     worker. What the PROVIDER sees on every authentication is that IP — which
//     is why provider crowding and the per-worker provider signals
//     (worker_provider_signals, migration 20260914150140) carry more weight here
//     than the band does.
//
// Nothing in this package does I/O or knows what a database is: a Candidate is
// measured facts, and scoring is a pure function of them, so the policy is
// unit-testable without Postgres and the SQL stays a plain aggregate.
package fleetscore

import (
	"cmp"
	"slices"
)

// Candidate is one live worker's measured state, as of the moment placement
// asked. Counts are MAILBOXES, not sends: this measures occupancy of a worker,
// not its throughput.
type Candidate struct {
	WorkerID string

	// The worker's current population, split by provider because an
	// API-backed mailbox and an SMTP+IMAP one occupy a worker very
	// differently (see MailboxWeight).
	SMTPMailboxes  int
	GmailMailboxes int
	M365Mailboxes  int

	// SameWorkspaceMailboxes is how many of the above belong to the workspace
	// being placed — the blast-radius input.
	SameWorkspaceMailboxes int
	// OtherBandMailboxes is how many belong to a DIFFERENT risk band than the
	// mailbox being placed — the band-conflict input. Expressed as conflict
	// rather than agreement so a worker carrying nothing is neutral rather than
	// penalised for having no band to agree with.
	OtherBandMailboxes int

	// Recent provider verdicts for the INCOMING mailbox's provider only. A
	// worker blocked by Google is still a fine home for an SMTP mailbox, so
	// health is judged per provider rather than per worker.
	ProviderOKEvents    int64
	ProviderBlockEvents int64
	// ProviderThrottleEvents counts the softer per-IP verdicts (rate_limited,
	// throttled, auth_failed). Recorded on the candidate because placement
	// SHOULD eventually price them; deliberately not yet a term, since a
	// throttle rate means nothing without the attempt count to divide it by and
	// inventing that denominator here would be a guess dressed as a measurement.
	ProviderThrottleEvents int64
}

// Incoming is the mailbox being placed. Its band is not a field: the band
// comparison is already folded into Candidate.OtherBandMailboxes by the query
// that measured it.
type Incoming struct {
	// Provider is the mailbox's transport leg ("smtp" | "gmail" | "m365").
	Provider string
}

// Ranked is a Candidate with the score it earned.
type Ranked struct {
	Candidate
	Score float64
}

// Policy holds the weights. It is a value, not a set of package constants, so a
// test can vary one number and the composition root could one day make them
// configurable — and so the relationships BETWEEN the weights (which are the
// actual design) can be asserted (TestWeightOrderingHoldsTheDesignInPlace).
type Policy struct {
	// TargetLoad is the weighted mailbox load one worker is AIMED at. It is a
	// target, never a ceiling: exceeding it costs OverloadWeight, it never makes
	// a worker ineligible.
	//
	// 40 is four SMTP mailboxes per concurrent task slot at the default
	// INROAD_WORKER_CONCURRENCY of 10. A mailbox is idle most of the time
	// (mailboxes.min_interval_seconds defaults to 120s), so oversubscribing the
	// slots four to one is conservative rather than optimistic. It is still a
	// guess, which is exactly why nothing here treats it as a limit.
	TargetLoad float64

	// SMTPMailboxWeight is what one SMTP+IMAP mailbox costs a worker: an IMAP
	// connection for reply/bounce polling plus a TLS SMTP session per send. The
	// unit of the whole load model, so it is 1 by definition.
	SMTPMailboxWeight float64
	// APIMailboxWeight is what one Gmail/Graph mailbox costs: HTTPS requests
	// over a pooled transport, no persistent IMAP leg at all. About a third of
	// an SMTP mailbox — not exactly a third, because an API mailbox still costs
	// token refreshes and per-request work. A single flat capacity only works if
	// mailboxes are weighted by what they actually consume; counting both as "1
	// mailbox" overprices an all-Gmail worker by roughly 3x and drives placement
	// away from the workers that have the most room.
	APIMailboxWeight float64

	// ProviderCrowdTarget is how many mailboxes of ONE provider a worker should
	// authenticate before crowding saturates. Providers rate-limit, challenge
	// and throttle per source address — that is the whole reason
	// worker_provider_signals is keyed by (worker, provider) — so this is the
	// term those signals exist to inform. 25 is a guess in the range where
	// per-IP auth limits start to bind; it is soft, and being over it costs at
	// most ProviderCrowdingWeight.
	//
	// Keyed on the provider LABEL, which is exact for gmail/m365 (one provider
	// really is one entity seeing one IP) and approximate for smtp, where the
	// mailboxes may point at many different relays. Refining the smtp case would
	// mean keying on mailboxes.smtp_host, which the signal vocabulary does not
	// record; until it does, the coarse label is the honest join.
	ProviderCrowdTarget float64

	// HeadroomWeight rewards spare capacity: the packing term, and the only
	// preference the pre-scoring assigner had.
	HeadroomWeight float64
	// OverloadWeight penalises projected load past TargetLoad, on a ramp. It
	// must exceed every other term combined, so that no mix of tenant, provider
	// and band preferences can talk placement into stacking one more mailbox
	// onto a worker that is already over target while a worker under target
	// exists.
	OverloadWeight float64
	// BlastRadiusWeight penalises concentrating ONE workspace on ONE worker. The
	// heaviest of the soft terms: if that egress IP goes bad, the concentrated
	// tenant goes off the air entirely, which is a worse outcome than any amount
	// of imbalance.
	BlastRadiusWeight float64
	// ProviderCrowdingWeight penalises piling one provider's mailboxes onto one
	// egress address. Below blast radius (crowding degrades a worker; a
	// concentrated tenant loses everything) and above the band, because unlike
	// the band it describes something the provider actually observes.
	ProviderCrowdingWeight float64
	// BandConflictWeight penalises co-locating risk bands. Deliberately the
	// weakest term there is: it is the only one whose causal story does not
	// reach a worker's egress IP (see the package doc). Weak is not zero — a
	// free tie-break toward segregation costs nothing.
	BandConflictWeight float64
}

// Default is the tuned policy placement uses. Every number is justified on its
// field; the ORDERING between them is the part that matters and the part
// TestWeightOrderingHoldsTheDesignInPlace pins.
func Default() Policy {
	return Policy{
		TargetLoad:             40,
		SMTPMailboxWeight:      1,
		APIMailboxWeight:       0.35,
		ProviderCrowdTarget:    25,
		HeadroomWeight:         10,
		OverloadWeight:         60,
		BlastRadiusWeight:      8,
		ProviderCrowdingWeight: 6,
		BandConflictWeight:     2,
	}
}

// MailboxWeight prices one mailbox by its provider. An unrecognised provider is
// priced as SMTP, matching mail.MultiSender's own dispatch rule: anything that
// is not "gmail" or "m365" takes the SMTP path, so pricing it as the cheap leg
// would under-count a real SMTP mailbox.
func (p Policy) MailboxWeight(provider string) float64 {
	switch provider {
	case "gmail", "m365":
		return p.APIMailboxWeight
	default:
		return p.SMTPMailboxWeight
	}
}

// Eligible reports whether a worker may take this placement at all. This is the
// ONLY hard gate, and it is deliberately narrow: a worker is excluded when
// EVERY recent thing this provider said to it was a refusal to talk to it
// (blocked / unreachable), and not excluded when it is also still completing
// operations. A worker with no recorded signals is NEW, not unhealthy — reading
// an empty window as a block would make a freshly added worker ineligible and
// defeat the one remedy for a saturated fleet.
func (c Candidate) Eligible() bool {
	return c.ProviderBlockEvents <= 0 || c.ProviderOKEvents > 0
}

// Rank scores every ELIGIBLE candidate and returns them best first. Ineligible
// candidates are dropped, so an empty result means a genuine refusal: nothing in
// the live fleet can serve this mailbox's provider right now.
//
// Ties resolve on worker_id ascending — the same deterministic rule every
// pre-scoring pick used, and the reason two callers racing the same first send
// converge on one worker instead of splitting a mailbox's mail across two egress
// IPs.
func (p Policy) Rank(candidates []Candidate, in Incoming) []Ranked {
	// The tenant's footprint across the live fleet is the blast-radius
	// denominator, and it is summed from the candidates themselves rather than
	// queried: a mailbox pinned to a worker that is no longer live is not part
	// of any blast radius placement can still affect.
	tenantFleet := 0
	for _, c := range candidates {
		tenantFleet += max(c.SameWorkspaceMailboxes, 0)
	}

	ranked := make([]Ranked, 0, len(candidates))
	for _, c := range candidates {
		if !c.Eligible() {
			continue
		}
		ranked = append(ranked, Ranked{Candidate: c, Score: p.score(c, in, tenantFleet)})
	}
	slices.SortFunc(ranked, func(a, b Ranked) int {
		if d := cmp.Compare(b.Score, a.Score); d != 0 {
			return d
		}
		return cmp.Compare(a.WorkerID, b.WorkerID)
	})
	return ranked
}

// score is the additive model. Each term is normalised to [0,1] first, so a
// weight is directly comparable to every other weight and the relationships
// asserted in the tests mean what they say.
//
// tenantFleetMailboxes is the workspace's footprint across the whole live
// fleet, which only Rank can know; it is a parameter rather than a field so
// Candidate stays a description of ONE worker.
func (p Policy) score(c Candidate, in Incoming, tenantFleetMailboxes int) float64 {
	incoming := p.MailboxWeight(in.Provider)
	projected := c.weightedLoad(p) + incoming
	// TargetLoad is a constant of the policy, but guard the division anyway: a
	// zero from a hand-built Policy would put NaN into a sort comparator and
	// make the winner depend on input order.
	utilisation := 0.0
	if p.TargetLoad > 0 {
		utilisation = projected / p.TargetLoad
	}

	headroom := clamp01(1 - utilisation)
	// Ramped, and saturating at twice the target: past that the worker has
	// already lost to anything under target, and letting the penalty grow
	// without bound would only make the logged scores harder to read.
	overload := clamp01(utilisation - 1)

	// Share of this tenant's live-fleet footprint that would sit on this worker
	// after the placement. A tenant with one mailbox scores the same everywhere
	// (it has nowhere to spread), which is correct: there is no blast radius to
	// reduce.
	tenantHere := float64(max(c.SameWorkspaceMailboxes, 0) + 1)
	blastRadius := clamp01(tenantHere / float64(max(tenantFleetMailboxes, 0)+1))

	crowding := 0.0
	if p.ProviderCrowdTarget > 0 {
		crowding = clamp01(float64(c.sameProviderMailboxes(in.Provider)+1) / p.ProviderCrowdTarget)
	}

	bandConflict := 0.0
	if total := c.totalMailboxes(); total > 0 {
		bandConflict = clamp01(float64(max(c.OtherBandMailboxes, 0)) / float64(total))
	}

	return p.HeadroomWeight*headroom -
		p.OverloadWeight*overload -
		p.BlastRadiusWeight*blastRadius -
		p.ProviderCrowdingWeight*crowding -
		p.BandConflictWeight*bandConflict
}

// weightedLoad is what this worker's current population costs it, in the same
// units as Policy.TargetLoad.
func (c Candidate) weightedLoad(p Policy) float64 {
	return float64(max(c.SMTPMailboxes, 0))*p.SMTPMailboxWeight +
		float64(max(c.GmailMailboxes, 0)+max(c.M365Mailboxes, 0))*p.APIMailboxWeight
}

func (c Candidate) totalMailboxes() int {
	return max(c.SMTPMailboxes, 0) + max(c.GmailMailboxes, 0) + max(c.M365Mailboxes, 0)
}

// sameProviderMailboxes counts how many mailboxes this worker already
// authenticates to the SAME provider as the one being placed. Unknown providers
// fall in with smtp, for the same reason MailboxWeight prices them there.
func (c Candidate) sameProviderMailboxes(provider string) int {
	switch provider {
	case "gmail":
		return max(c.GmailMailboxes, 0)
	case "m365":
		return max(c.M365Mailboxes, 0)
	default:
		return max(c.SMTPMailboxes, 0)
	}
}

func clamp01(v float64) float64 {
	return min(max(v, 0), 1)
}
