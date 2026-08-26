package warmup

// Reporting placement by content version.
//
// # This gates NOTHING, and the reason is recorded rather than assumed
//
// No threshold, lane, health state or promotion decision reads a per-version rate,
// and the reason is NOT the reason slices A and B had (a tab and an
// Authentication-Results verdict are structurally unobservable on a whole provider
// class, so gating them would penalise that class forever). It is not quite slice C's
// either. Two problems stack here, and both are calibration problems — invariants
// 57-60 are the precedent for recording WHICH one applies:
//
//  1. The SAMPLE is necessarily small. The library is shared across the whole pool
//     and split by (template, turn), so every cell is a fraction of a fraction of one
//     workspace's 7-day window. Most versions will sit under MinPlacementSamples
//     permanently in a small pool.
//  2. The rate is CONFOUNDED with the senders. Content is not assigned to mailboxes
//     experimentally — a template's apparent spam rate is whatever the mailboxes that
//     happened to send it were already experiencing. A degrading mailbox that draws one
//     template repeatedly makes that template look toxic; retiring it would change
//     nothing and would destroy corpus diversity, which content.go says is itself a
//     deliverability property.
//
// So this reports, and something else decides. Separating the two signals is worth
// doing on its own: an operator looking at a spike can now see whether it tracks one
// template or one mailbox, which is a question the pooled number could not be asked.
//
// Before anything gates on this, both problems have to be answered — a controlled
// assignment that breaks the confound, and enough volume for the cells to clear a
// floor. Unlike the unobservability reasons, these expire; that is why they are
// written down.

// ContentVersionStat is one content version's trailing-window placement counts, as
// the workspace aggregation returns them.
//
// Inbox counts the inbox-side landings (including tabbed ones — a tabbed message
// landed in the inbox) and Spam the junked ones, exactly as every other placement
// counter in this subsystem defines them. Neither counts placements that were only
// scoreable as 'other'.
//
// An empty Version is not a defect: it is every observation recorded before this
// slice, plus any whose send predates it. It is reported as its own bucket.
type ContentVersionStat struct {
	Version string
	Inbox   int
	Spam    int
}

// ContentVersionPlacement is one version's reported placement: its counts, ITS OWN
// denominator, and the rates over that denominator when it has enough of one.
//
// InboxRate/SpamRate are nil when the rate is not established. The nil IS the verdict
// — there is deliberately no second boolean that could disagree with it.
type ContentVersionPlacement struct {
	Version string
	Inbox   int
	Spam    int
	// PlacementSample is Inbox+Spam for THIS version alone. It is reported even when
	// it is too small to carry a rate, because "we have barely measured this template"
	// is the most useful thing the report says about most templates.
	PlacementSample int
	InboxRate       *float64
	SpamRate        *float64
}

// FoldContentVersions turns per-version counts into per-version rates, preserving the
// order the caller supplied (the query orders by version, so it is stable).
//
// Every rate is computed over the version's OWN sample and never over the population
// total. This is the fifth place that rule has had to be applied in this subsystem —
// after bounce populations, tab capability, per-route and per-observer — so it is the
// whole reason this fold exists as a tested function instead of three lines inlined at
// the boundary: pooling here would report a clean template as failing and a failing
// one as clean, which is worse than reporting nothing.
//
// MinPlacementSamples is REUSED rather than a per-version minimum invented. The
// question "is this rate proven" does not change because the population was split, and
// a second threshold would be a second thing to keep in step with the first.
//
// Pure: no clock, no I/O.
func FoldContentVersions(stats []ContentVersionStat) []ContentVersionPlacement {
	out := make([]ContentVersionPlacement, 0, len(stats))
	for _, s := range stats {
		sample := s.Inbox + s.Spam
		p := ContentVersionPlacement{
			Version:         s.Version,
			Inbox:           s.Inbox,
			Spam:            s.Spam,
			PlacementSample: sample,
		}
		// This is the ONLY gate, and it is deliberately the only one. A sample of 0
		// (every placement was scoreable only as 'other') takes the same branch, so
		// the positive floor is also what makes the division below safe — there is no
		// second divide-by-zero check that could disagree with it, and no unreachable
		// branch pretending to be a safety net.
		if sample >= MinPlacementSamples {
			p.InboxRate = contentVersionRate(s.Inbox, sample)
			p.SpamRate = contentVersionRate(s.Spam, sample)
		}
		out = append(out, p)
	}
	return out
}

// contentVersionRate is n/denom as a fraction. Reached only for denom >=
// MinPlacementSamples, which is what keeps it from dividing by zero.
func contentVersionRate(n, denom int) *float64 {
	rate := float64(n) / float64(denom)
	return &rate
}
