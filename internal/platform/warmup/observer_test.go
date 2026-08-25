package warmup

import (
	"fmt"
	"math"
	"testing"
)

func obs(id, cohort string, spam, total int) ObserverStats {
	return ObserverStats{ObserverMailboxID: id, Cohort: cohort, Spam: spam, Total: total}
}

// peers returns n ordinary observers in one cohort, each reporting `spam` of 100.
func peers(cohort string, n, spam int) []ObserverStats {
	out := make([]ObserverStats, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, obs(fmt.Sprintf("%s-peer-%d", cohort, i), cohort, spam, 100))
	}
	return out
}

func discountedIDs(t *testing.T, in []ObserverStats) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for _, d := range DiscountObservers(in) {
		got[d.ObserverMailboxID] = true
	}
	return got
}

// The hole this closes: one mailbox junking everything it receives degrades every
// sender that mails it.
func TestDiscountObserversExcludesAHostileReporter(t *testing.T) {
	in := append(peers("google", 5, 4), obs("hostile", "google", 44, 50))

	got := DiscountObservers(in)

	if len(got) != 1 || got[0].ObserverMailboxID != "hostile" {
		t.Fatalf("got %+v, want only the hostile observer", got)
	}
	d := got[0]
	if math.Abs(d.SpamRate-0.88) > 0.001 {
		t.Errorf("spam rate = %v, want 0.88", d.SpamRate)
	}
	if math.Abs(d.CohortSpamRate-0.04) > 0.001 {
		t.Errorf("cohort rate = %v, want 0.04 — the peers WITHOUT the hostile observer", d.CohortSpamRate)
	}
	if math.Abs(d.Lift-22) > 0.01 {
		t.Errorf("lift = %v, want 22", d.Lift)
	}
}

// THE guard against over-exclusion, which is the worse failure: dropping a
// legitimately strict observer removes real spam evidence and makes every sender
// that mails it look cleaner than it is.
//
// Microsoft junks materially more than Google. Compared against a pooled rate an
// ordinary M365 mailbox looks hostile; compared against its OWN provider it is
// unremarkable. The cohort is what makes that distinction, and this fixture would
// flag it if the cohort were dropped.
func TestDiscountObserversJudgesAnObserverAgainstItsOwnProvider(t *testing.T) {
	// The Google side must DOMINATE the volume, or the Microsoft peers drag the
	// pooled baseline up far enough that pooling and cohorting agree and the fixture
	// discriminates nothing. Twenty clean Google mailboxes against two Microsoft
	// ones: pooled, the baseline is 5% and the strict observer reads as 8x hostile;
	// against its own provider it is 1.1x and unremarkable.
	in := peers("google", 20, 2)
	in = append(in, peers("microsoft", 2, 40)...)
	in = append(in, obs("strict-m365", "microsoft", 45, 100)) // strict, and normal for its provider

	if got := discountedIDs(t, in); got["strict-m365"] {
		t.Errorf("a normal Microsoft observer was discounted by comparison with Google: %+v",
			DiscountObservers(in))
	}
}

// The absolute floor, and why it is not redundant with the multiple. On a healthy
// pool a cohort sits near zero, so ANY spam-reporting observer clears 3x — the
// cleaner the pool, the more easily an ordinary mailbox trips it.
func TestDiscountObserversNeedsAnAbsoluteRateNotJustAMultiple(t *testing.T) {
	// 12% against a 1% cohort is 12x, and 12% is not evidence of a hostile mailbox.
	in := append(peers("google", 6, 1), obs("slightly-high", "google", 12, 100))

	if got := discountedIDs(t, in); got["slightly-high"] {
		t.Errorf("12%% reporting was discounted on a 1%% cohort; the multiple alone flags "+
			"ordinary variation on clean pools: %+v", DiscountObservers(in))
	}
}

// A cohort of one has nothing to be an outlier against, and treating it as hostile
// would discard the pool's only evidence about that provider.
func TestDiscountObserversNeverJudgesACohortOfOne(t *testing.T) {
	in := append(peers("google", 5, 3), obs("lonely", "microsoft", 90, 100))

	if got := discountedIDs(t, in); got["lonely"] {
		t.Errorf("the only observer on its provider was discounted, discarding the pool's "+
			"only evidence about that provider: %+v", DiscountObservers(in))
	}
}

// An observer that dominates its cohort would otherwise raise the very baseline it is
// measured against and hide itself — worst where the cohort is small, which is where
// one bad observer does the most damage.
func TestDiscountObserversExcludesItselfFromItsOwnBaseline(t *testing.T) {
	// Two clean peers (the minimum baseline), and a hostile observer carrying five
	// times their combined volume. Pooled, the cohort rate is dragged up to ~75% and
	// the hostile observer looks typical.
	in := []ObserverStats{
		obs("clean-a", "google", 1, 100),
		obs("clean-b", "google", 1, 100),
		obs("hostile", "google", 900, 1000),
	}

	got := discountedIDs(t, in)
	if !got["hostile"] {
		t.Errorf("a dominant hostile observer hid inside its own baseline: %+v", DiscountObservers(in))
	}
	if got["clean"] {
		t.Error("the clean peer was discounted")
	}
}

func TestDiscountObserversThresholds(t *testing.T) {
	tests := []struct {
		name              string
		spam, total       int
		peerSpam, peerObs int
		want              bool
	}{
		// Nineteen observations is noise however they fell.
		{"below the sample floor", 19, 19, 2, 5, false},
		{"at the sample floor", 20, 20, 2, 5, true},
		// 30% is the floor; 29% of an equally extreme record is not.
		{"just below the absolute rate floor", 29, 100, 1, 5, false},
		{"at the absolute rate floor", 30, 100, 1, 5, true},
		// 30% against a 12% cohort is 2.5x — high, and not three times.
		{"above the floor but below the multiple", 30, 100, 12, 5, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := append(peers("google", tc.peerObs, tc.peerSpam), obs("subject", "google", tc.spam, tc.total))
			if got := discountedIDs(t, in)["subject"]; got != tc.want {
				t.Errorf("discounted = %v, want %v (%d/%d against %d peers at %d/100)",
					got, tc.want, tc.spam, tc.total, tc.peerObs, tc.peerSpam)
			}
		})
	}
}

// A spotless cohort makes any spam-reporting observer infinitely worse than its
// peers. True, and unserialisable — so the same continuity correction the incident
// fold uses keeps it finite and monotone.
func TestDiscountObserversScoresASpotlessCohortFinitely(t *testing.T) {
	in := append(peers("google", 5, 0), obs("hostile", "google", 90, 100))

	got := DiscountObservers(in)
	if len(got) != 1 {
		t.Fatalf("got %+v, want the hostile observer discounted", got)
	}
	if math.IsInf(got[0].Lift, 0) || math.IsNaN(got[0].Lift) {
		t.Errorf("lift = %v, which cannot be serialised into JSON", got[0].Lift)
	}
}

func TestDiscountObserversIsSortedWorstFirst(t *testing.T) {
	in := peers("google", 6, 1)
	in = append(in, obs("bad", "google", 80, 100), obs("worse", "google", 95, 100))

	got := DiscountObservers(in)
	if len(got) != 2 {
		t.Fatalf("got %+v, want both discounted", got)
	}
	if got[0].ObserverMailboxID != "worse" {
		t.Errorf("order = %v, %v; want the worse observer first", got[0].ObserverMailboxID, got[1].ObserverMailboxID)
	}
}

// Callers bind the result as a SQL array parameter, where nil and empty differ.
func TestDiscountedObserverIDsIsNeverNil(t *testing.T) {
	if ids := DiscountedObserverIDs(DiscountObservers(nil)); ids == nil {
		t.Error("DiscountedObserverIDs returned nil; the caller binds this as a uuid[] and " +
			"an empty exclusion list must be an empty array, not NULL")
	}
	if ids := DiscountedObserverIDs(nil); len(ids) != 0 {
		t.Errorf("DiscountedObserverIDs(nil) = %v, want empty", ids)
	}
}

// The healthy case, stated because it is the one that must never regress: a pool
// where nobody is an outlier discounts nobody, so the evidence the policy reads is
// byte-for-byte what it was before this axis existed.
func TestDiscountObserversLeavesAnOrdinaryPoolAlone(t *testing.T) {
	in := peers("google", 8, 5)
	in = append(in, peers("microsoft", 5, 22)...)

	if got := DiscountObservers(in); len(got) != 0 {
		t.Errorf("an ordinary pool discounted %+v", got)
	}
}

// Two outliers in one cohort inflate each other's baseline, so a milder one can hide
// behind an extreme one: at 40% against peers dragged to 14% by a 95% neighbour, the
// multiple lands at 2.8 and the milder observer survives.
//
// Left as it is, deliberately. The error runs toward UNDER-exclusion — evidence is
// kept that might have been dropped — and that is the safe direction here, because
// dropping a legitimate observer removes real spam evidence and flatters every sender
// that mails it. A median-of-peers baseline would be more robust and would also make
// a small cohort's verdict hinge on one member; not worth trading while the failure
// mode is "we kept some evidence".
func TestDiscountObserversLetsAMilderOutlierHideBehindAnExtremeOne(t *testing.T) {
	in := peers("google", 6, 1)
	in = append(in, obs("mild", "google", 40, 100), obs("extreme", "google", 95, 100))

	got := discountedIDs(t, in)
	if !got["extreme"] {
		t.Error("the extreme observer was not discounted")
	}
	if got["mild"] {
		t.Error("the milder observer was discounted; this test documents that it is NOT, " +
			"and if that changed the comment above should change with it")
	}
}

// The dilution attack, and the reason nothing gates on this yet.
//
// An attacker who adds clean volume to a cohort drags the baseline down until an
// HONEST observer clears the multiple — silencing the mailbox that would have
// reported their spam. Confirmed against the shipped detector before the peer floors
// existed: 150 clean observations turned an honest 35/100 observer, sitting beside a
// genuinely strict 25/100 peer, into a discounted one at lift 3.50.
//
// The floors below raise the price (the attacker now needs two mailboxes and real
// volume) but do NOT close it, which is why the result is disclosed and not applied.
func TestDiscountObserversStillYieldsToCohortDilution(t *testing.T) {
	honest := obs("honest", "microsoft", 35, 100)
	strict := obs("strict-peer", "microsoft", 25, 100)
	if got := DiscountObservers([]ObserverStats{honest, strict}); len(got) != 0 {
		t.Fatalf("an honest observer was discounted before any dilution: %+v", got)
	}

	diluted := DiscountObservers([]ObserverStats{
		honest, strict,
		obs("attacker-a", "microsoft", 0, 150),
		obs("attacker-b", "microsoft", 0, 150),
	})
	var silenced bool
	for _, d := range diluted {
		if d.ObserverMailboxID == "honest" {
			silenced = true
		}
	}
	if !silenced {
		t.Skip("dilution no longer silences an honest observer — if this became true on " +
			"purpose, the gate deferred in security.md invariant 59 can be reconsidered")
	}
}

// A single peer is not a baseline. Five clean observations from one mailbox could
// otherwise delete an observer's entire spam record.
func TestDiscountObserversNeedsMoreThanOnePeerMailbox(t *testing.T) {
	// The lone peer carries 100 observations, so the SAMPLE floor is satisfied and
	// only the mailbox floor can refuse this — otherwise the two guards overlap and
	// neither is tested.
	in := []ObserverStats{obs("subject", "google", 30, 100), obs("lone-peer", "google", 0, 100)}

	if got := discountedIDs(t, in); got["subject"] {
		t.Errorf("a single peer mailbox was treated as a baseline: %+v", DiscountObservers(in))
	}
}

// And the peers must clear the same sample floor the accused does.
func TestDiscountObserversNeedsAPeerBaselineWorthComparingTo(t *testing.T) {
	in := []ObserverStats{
		obs("subject", "google", 30, 100),
		obs("peer-a", "google", 0, 8),
		obs("peer-b", "google", 0, 8),
	}

	if got := discountedIDs(t, in); got["subject"] {
		t.Errorf("sixteen peer observations were treated as a baseline: %+v", DiscountObservers(in))
	}
}

// `unknown` is the destination_esp DEFAULT, so every observation written before
// migration 000062 carries it, as does any mailbox the MX sweep has not reached.
// Judging that bucket pools Google against Microsoft — the exact comparison the
// cohort exists to prevent. DetectIncidents already refuses it and gates nothing.
func TestDiscountObserversNeverJudgesAnUnresolvedCohort(t *testing.T) {
	for _, unresolved := range []string{"unknown", "", "  ", "UNKNOWN"} {
		t.Run(unresolved, func(t *testing.T) {
			in := []ObserverStats{
				obs("hostile", unresolved, 90, 100),
				obs("peer-a", unresolved, 1, 100),
				obs("peer-b", unresolved, 1, 100),
			}
			if got := DiscountObservers(in); len(got) != 0 {
				t.Errorf("judged the unresolved cohort %q: %+v", unresolved, got)
			}
		})
	}
}
