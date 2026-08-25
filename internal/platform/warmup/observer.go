package warmup

import "sort"

// Observer trust: whose reports count as evidence.
//
// Placement is SENDER-attributed — a message landing in spam degrades whoever sent
// it — but it is RECIPIENT-observed. Until now every observer counted equally, so a
// single mailbox that reports everything it receives as spam degrades every sender
// that mails it. That is the one attribution hole invariant 52 does not already
// close: the token binds the observation to a real send, and the SQL re-proves the
// send↔recipient pair, but nothing questions the recipient's own verdict.
//
// The mailbox does not have to be malicious for this to bite. A misconfigured
// aggressive spam filter, a mailbox whose owner bulk-junked a folder, or one
// compromised account produce the same signal.

// The trust thresholds. All three must be met before an observer's reports are
// discounted, because the failure mode of over-exclusion is worse than the hole:
// dropping a legitimately strict observer removes real spam evidence and makes
// every sender that mails it look cleaner than it is.
const (
	// MinObserverSamples is how much a mailbox must have observed before its rate
	// means anything. A mailbox that saw four messages and junked three is noise.
	MinObserverSamples = 20
	// ObserverSpamLift is how many times the comparable cohort's spam rate an
	// observer must report before it is treated as an outlier rather than a strict
	// provider.
	ObserverSpamLift = 3.0
	// MinObserverSpamRate is an ABSOLUTE floor, and it is the guard that stops this
	// firing on healthy pools. Where a cohort sits at 1%, three times that is 3% —
	// well within normal variation and not evidence of anything. Without this, the
	// cleaner the pool, the more easily an ordinary observer trips the multiple.
	MinObserverSpamRate = 0.30
)

// ObserverStats is one observer's reporting record over the window, alongside the
// cohort it is compared against.
//
// Cohort is the observer's own receiving provider (warmup_observations.destination_esp,
// which slice C derived from the RECIPIENT mailbox). Comparing across providers would
// be unfair in a way that matters: Microsoft junks materially more than Google, so a
// pooled comparison would flag every M365 mailbox in a mostly-Google pool as hostile.
type ObserverStats struct {
	ObserverMailboxID string
	Cohort            string
	Spam              int
	Total             int
}

// DiscountedObserver is one observer whose reports will not count, with the
// arithmetic that decided it — surfaced rather than applied silently, because
// discarding evidence an operator cannot see is how a reputation engine quietly
// starts lying.
type DiscountedObserver struct {
	ObserverMailboxID string
	Cohort            string
	Spam              int
	Total             int
	SpamRate          float64
	CohortSpamRate    float64
	Lift              float64
}

// DiscountObservers returns the observers whose placement reports must be excluded
// from every sender's evidence. Pure: no clock, no I/O, no DB.
//
// **This one DOES gate**, unlike Phase 2's slices, and deliberately so: it removes
// attacker-influenceable evidence rather than adding a threshold on top of it, which
// is the direction invariant 52 asks for. Excluding a wild outlier is defensible
// without knowing what a normal rate looks like — which is why this does not wait for
// the calibration data an exposure budget needs.
//
// The cohort rate an observer is measured against EXCLUDES that observer. Otherwise a
// mailbox that dominates its cohort raises the very baseline it is being compared to
// and hides itself — worst exactly where the cohort is small, which is where a single
// bad observer does the most damage.
//
// A cohort of one is never judged. There is nothing to compare against, and treating
// "the only Microsoft mailbox in the pool" as an outlier would discard the pool's
// only evidence about Microsoft.
func DiscountObservers(observers []ObserverStats) []DiscountedObserver {
	type totals struct{ spam, total int }
	cohorts := map[string]totals{}
	for _, o := range observers {
		t := cohorts[o.Cohort]
		t.spam += o.Spam
		t.total += o.Total
		cohorts[o.Cohort] = t
	}

	out := []DiscountedObserver{}
	for _, o := range observers {
		if o.Total < MinObserverSamples {
			continue
		}
		rate := float64(o.Spam) / float64(o.Total)
		if rate < MinObserverSpamRate {
			continue
		}

		// The cohort WITHOUT this observer.
		peers := cohorts[o.Cohort]
		peerSpam, peerTotal := peers.spam-o.Spam, peers.total-o.Total
		// A cohort of one: nothing to be an outlier against.
		//
		// OVER-DETERMINED TODAY, and kept — the same shape as the incident fold's
		// empty-outside guard. With no peers the continuity-corrected rate becomes
		// 0.5/0 = +Inf and the lift collapses to 0, which fails the bar anyway;
		// verified by reverting this, and no test noticed. It stays because relying
		// on IEEE infinity for a correctness property is not a design, and because
		// this is the one thing that would still hold if ObserverSpamLift were ever
		// lowered to zero.
		if peerTotal <= 0 {
			continue
		}
		peerRate := float64(peerSpam) / float64(peerTotal)

		// A spotless cohort makes any spam-reporting observer infinitely worse than
		// its peers, which is true but unserialisable. Same continuity correction the
		// incident fold uses: zero out of N scored as half a case out of N.
		peerCases := float64(peerSpam)
		if peerCases == 0 {
			peerCases = 0.5
		}
		lift := rate / (peerCases / float64(peerTotal))
		if lift < ObserverSpamLift {
			continue
		}
		out = append(out, DiscountedObserver{
			ObserverMailboxID: o.ObserverMailboxID, Cohort: o.Cohort,
			Spam: o.Spam, Total: o.Total,
			SpamRate: rate, CohortSpamRate: peerRate, Lift: lift,
		})
	}

	// Worst first, then a total order, so the caller's array parameter and any UI
	// see the same sequence for the same input.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lift != out[j].Lift {
			return out[i].Lift > out[j].Lift
		}
		return out[i].ObserverMailboxID < out[j].ObserverMailboxID
	})
	return out
}

// DiscountedObserverIDs is the id list in the order DiscountObservers returned,
// which is what the snapshot refresh binds as its exclusion array.
func DiscountedObserverIDs(discounted []DiscountedObserver) []string {
	ids := make([]string, 0, len(discounted))
	for _, d := range discounted {
		ids = append(ids, d.ObserverMailboxID)
	}
	return ids
}
