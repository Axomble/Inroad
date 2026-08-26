package rotation

import (
	"math"
	"sort"
)

// Exposure budgets: how much of a campaign's sending may depend on any one thing
// that can fail all at once.
//
// The reputation engine's other controls are REACTIVE — they read evidence and
// contain a mailbox that is already degrading. This one is structural and reads no
// evidence at all. If 68% of a campaign's volume signs as one DKIM domain, then a
// key rotation gone wrong, a DNS edit, or that domain landing on a blocklist costs
// 68% of the campaign's sending, and no amount of per-mailbox health watching would
// have seen it coming. Concentration is a risk you can measure before anything is
// wrong, which is exactly why it is worth limiting before anything is wrong.
//
// Deliberately NOT a reputation signal. It reads the distribution of the operator's
// own sending across identities from the operator's own records — never an observed
// rate, never anything a message carried. That is what keeps it outside the
// influence security.md invariants 57, 58 and 59 constrain: an attacker cannot make
// a workspace's sending more concentrated than the operator configured it.

// MaxFaultDomainShare is the most of a campaign's assigned volume that may rest on
// one fault domain while an alternative exists.
//
// A guess, and one worth making, because the cost of guessing wrong here is
// unusually low: this narrows a candidate list that is ALREADY eligible, so being
// wrong picks a different healthy mailbox. It never withholds a send, never changes
// a health state, and never reduces volume — see WithinExposureBudget's refusal to
// empty the set. Contrast a reputation threshold set without calibration, where
// being wrong stops mail.
//
// 0.6 rather than an even split, because an even split is not the goal. Two
// mailboxes on two domains should be free to send 60/40 as capacity and rotation
// dictate; what this exists to prevent is 95/5, where the pool looks diversified
// and is not.
const MaxFaultDomainShare = 0.6

// FaultDomainOf names the thing a candidate's deliverability depends on and which
// can fail for all its members at once — the sending domain, or the organizational
// domain of the mailbox address. Supplied by the caller rather than derived here:
// this package must not learn what a sending domain is, and the caller already has
// the mailbox row.
//
// An empty domain means "unknown", and unknown is never grouped. Bucketing
// unclassified mailboxes together would invent a shared fault domain out of our own
// ignorance and then act on it, which is the mistake the incident fold and the
// observer detector each had to be corrected for.
type FaultDomainOf func(candidate Candidate) string

// WithinExposureBudget narrows candidates to those whose fault domain is not
// already carrying more than MaxFaultDomainShare of the campaign's assigned volume.
//
// **It never returns an empty set.** If every remaining candidate is over budget —
// which is the ordinary case for the single-domain workspace this product is mostly
// used by — the budget is unsatisfiable and the full set is returned unchanged. A
// concentration limit that could stop sending would be a worse bug than the
// concentration it prevents: the operator would lose mail today to avoid losing mail
// if something failed later.
//
// Shares are computed over AssignedCount, which is the campaign's own history and
// therefore the "current usage" half of a traffic limit. A campaign with no
// assignments yet has no concentration to correct and is returned unchanged.
func WithinExposureBudget(candidates []Candidate, domainOf FaultDomainOf) []Candidate {
	return WithinExposureBudgetFor(candidates, domainOf, nil)
}

// WithinExposureBudgetFor is WithinExposureBudget with a PER-DOMAIN ceiling, which is
// the reactive half: a fault domain the engine already considers degraded should
// carry less of the campaign than a healthy one, not the same share until the moment
// it is cut off entirely.
//
// That step is the gap in the current controls. warmup.LaneMaySend is a breaker — a
// quarantined domain sends nothing — but between "fine" and "cut off" there is
// nothing, so a domain in `watch` carries full concentration right up until it does
// not. A budget is the graduated form the design asks for alongside the breaker.
//
// ceilingOf may be nil, which means the flat MaxFaultDomainShare everywhere. A
// ceiling it does not recognise, or one outside (0, 1], falls back to the flat cap:
// this function narrows an eligible list and must never be the thing that decides a
// domain cannot send. Containment is LaneMaySend's job and stays there, because a
// second implementation of "may this send" is the shape every repeated defect in this
// subsystem has taken.
func WithinExposureBudgetFor(candidates []Candidate, domainOf FaultDomainOf, ceilingOf func(domain string) float64) []Candidate {
	if len(candidates) < 2 || domainOf == nil {
		return candidates
	}

	assigned := map[string]int64{}
	var total int64
	for _, c := range candidates {
		total += c.AssignedCount
		if d := domainOf(c); d != "" {
			assigned[d] += c.AssignedCount
		}
	}
	if total == 0 {
		return candidates // nothing sent yet: no distribution to be lopsided
	}

	kept := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		d := domainOf(c)
		// An unknown domain is not over budget, because it is not known to share a
		// failure with anything. Grouping the unclassified together and throttling
		// them would be acting on our own ignorance.
		if d == "" {
			kept = append(kept, c)
			continue
		}
		if float64(assigned[d])/float64(total) <= ceilingFor(ceilingOf, d) {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return candidates
	}
	return kept
}

// ceilingFor resolves one domain's share ceiling, defaulting to the flat cap.
//
// An out-of-range answer is treated as no answer rather than clamped. A ceiling of 0
// would mean "this domain may not send", which is a containment decision this package
// does not make and LaneMaySend already does; silently clamping it to something tiny
// would let a caller express that by accident and get most of the way there.
func ceilingFor(ceilingOf func(domain string) float64, domain string) float64 {
	if ceilingOf == nil {
		return MaxFaultDomainShare
	}
	c := ceilingOf(domain)
	// NaN needs its own arm: every comparison against it is false, so `c <= 0 || c > 1`
	// lets it straight through. The narrowing then survives by accident — every
	// `share <= NaN` is false, everything is dropped, and the never-empty fallback
	// hands the set back — but the value also reaches the wire, and encoding/json
	// REFUSES to marshal NaN, so one bad ceiling would fail the whole senders response
	// rather than one row. Not reachable today (warmup.ExposureCeiling returns
	// constants or 0); one condition is cheaper than trusting that it stays so.
	if math.IsNaN(c) || c <= 0 || c > 1 {
		return MaxFaultDomainShare
	}
	return c
}

// FaultDomainShares reports each fault domain's share of the assigned volume,
// worst first — the "current usage" an operator sees next to the limit. Domains are
// returned even when they are within budget, because a budget with no visible usage
// is a number nobody can act on.
func FaultDomainShares(candidates []Candidate, domainOf FaultDomainOf) []FaultDomainShare {
	return FaultDomainSharesFor(candidates, domainOf, nil)
}

// FaultDomainSharesFor is FaultDomainShares against the SAME per-domain ceilings the
// selector applied, and it exists because the flat-cap version quietly lied.
//
// A domain in `recovery` is narrowed away at 0.20 while MaxFaultDomainShare is 0.60,
// so reporting OverBudget against the flat cap showed a domain the selector was
// actively throttling as comfortably within budget. An operator reconciling the two
// would have found the pool avoiding a domain the usage panel called fine. Each row
// now carries the ceiling it was judged against, so the number and the verdict cannot
// drift apart.
func FaultDomainSharesFor(candidates []Candidate, domainOf FaultDomainOf, ceilingOf func(domain string) float64) []FaultDomainShare {
	if domainOf == nil {
		return []FaultDomainShare{}
	}
	assigned := map[string]int64{}
	var total int64
	for _, c := range candidates {
		total += c.AssignedCount
		if d := domainOf(c); d != "" {
			assigned[d] += c.AssignedCount
		}
	}
	out := make([]FaultDomainShare, 0, len(assigned))
	for domain, n := range assigned {
		share := 0.0
		if total > 0 {
			share = float64(n) / float64(total)
		}
		ceiling := ceilingFor(ceilingOf, domain)
		out = append(out, FaultDomainShare{
			Domain: domain, Assigned: n, Share: share,
			Ceiling: ceiling, OverBudget: share > ceiling,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Share != out[j].Share {
			return out[i].Share > out[j].Share
		}
		return out[i].Domain < out[j].Domain
	})
	return out
}

// FaultDomainShare is one fault domain's slice of a campaign's assigned volume.
type FaultDomainShare struct {
	Domain   string
	Assigned int64
	Share    float64
	// Ceiling is the share this domain was judged against — the flat cap, or a lower
	// one because the domain is degrading. Published rather than assumed, so a reader
	// can see WHY a 25% domain is over budget when another at 55% is not.
	Ceiling    float64
	OverBudget bool
}
