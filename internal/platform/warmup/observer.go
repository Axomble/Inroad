package warmup

import (
	"sort"
	"strings"
)

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
	// MinObserverPeers is how many OTHER mailboxes must be in the cohort before it is
	// a baseline rather than one mailbox's opinion. A single peer carrying five clean
	// observations could otherwise condemn an observer with a full record.
	MinObserverPeers = 2
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
// **This gates NOTHING, and that is a reversal.** It was written to remove hostile
// reports from the evidence the health policy reads, which is the direction invariant
// 52 asks for. A security audit killed that, and the reason is worth keeping because
// it will be the same reason next time.
//
// The cohort is DILUTABLE. An attacker who adds clean volume to a cohort drags the
// peer baseline down until an HONEST observer clears the multiple — silencing the
// mailbox that would have reported their spam and flattering their own sender.
// Reproduced before the peer floors below existed: 150 clean observations discounted
// an honest 35/100 observer sitting beside a genuinely strict 25/100 peer.
//
// Under-containment is the dangerous direction in a reputation engine. The hole this
// was meant to close makes senders look WORSE than they are — visible, self-limiting,
// and it costs sending. The gate would have added a way to make them look BETTER than
// they are, silently, which is the failure this subsystem exists to prevent. So the
// verdicts are published and applied to nothing.
//
// The cohort is also route-derived (destination_esp), and security.md invariant 57
// says in as many words that nothing gating may read a per-route rate. Gating here
// would have breached an invariant written two slices ago for exactly this case.
//
// What the gate needs before it can land: a cohort key bound to something the
// attacker does not control. See invariant 59.
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
	type totals struct{ spam, total, mailboxes int }
	cohorts := map[string]totals{}
	for _, o := range observers {
		// An unresolved cohort is not a cohort. `unknown` is the destination_esp
		// column's DEFAULT, so every observation written before migration 000062
		// carries it, as does any mailbox whose domain the MX sweep has not reached
		// — pooling those judges a Google mailbox against a Microsoft one, which is
		// exactly the unfair comparison the cohort exists to prevent. DetectIncidents
		// already refuses this and gates nothing; refusing it here matters more.
		if unresolvedDimensionValues[strings.ToLower(strings.TrimSpace(o.Cohort))] {
			continue
		}
		t := cohorts[o.Cohort]
		t.spam += o.Spam
		t.total += o.Total
		t.mailboxes++
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
		// The baseline must be evidence, not one mailbox's opinion. Both floors are
		// the accused's own floors applied to the peers, and their absence was a real
		// hole: five clean observations from a single peer could condemn an observer
		// with a full record, worst exactly where a bad observer does the most damage.
		//
		// This does NOT close cohort dilution — an attacker with enough clean volume
		// still drags the baseline down — which is why nothing gates on this. See the
		// contract note above.
		if peers.mailboxes-1 < MinObserverPeers || peerTotal < MinObserverSamples {
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
