package warmup

import (
	"math"
	"testing"
)

// rateOf reads a reported rate, failing the test when it is not established. Keeps
// every assertion below about the ARITHMETIC rather than about nil handling.
func rateOf(t *testing.T, p ContentVersionPlacement, which string, r *float64) float64 {
	t.Helper()
	if r == nil {
		t.Fatalf("%s rate for version %q is not established, but its sample is %d (floor is %d)",
			which, p.Version, p.PlacementSample, MinPlacementSamples)
	}
	return *r
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// THE rule this subsystem keeps getting wrong: each version's rate is computed over
// ITS OWN denominator, never over the pooled total.
//
// The fixture is chosen so a pooled computation is VISIBLY different rather than
// merely differently rounded. Version A is 40 clean; version B is 30 spam. Per
// version that is a 0% and a 100% spam rate. Pooled, both would read 30/70 ≈ 43%,
// which would report the clean template as failing and the failing one as fine — the
// single most misleading answer available.
func TestFoldComputesEachVersionsRateOverItsOwnDenominator(t *testing.T) {
	got := FoldContentVersions([]ContentVersionStat{
		{Version: "sl1:aaaaaaaaaaaaaaaa", Inbox: 40, Spam: 0},
		{Version: "sl1:bbbbbbbbbbbbbbbb", Inbox: 0, Spam: 30},
	})
	if len(got) != 2 {
		t.Fatalf("folded %d versions, want 2", len(got))
	}

	clean, dirty := got[0], got[1]
	if clean.Version != "sl1:aaaaaaaaaaaaaaaa" || dirty.Version != "sl1:bbbbbbbbbbbbbbbb" {
		t.Fatalf("versions came back as %q, %q — the fold reordered or renamed them",
			clean.Version, dirty.Version)
	}
	if clean.PlacementSample != 40 || dirty.PlacementSample != 30 {
		t.Fatalf("samples are %d and %d, want 40 and 30: a version's sample is its own inbox+spam, "+
			"not the population's", clean.PlacementSample, dirty.PlacementSample)
	}
	if r := rateOf(t, clean, "spam", clean.SpamRate); !closeTo(r, 0) {
		t.Errorf("clean version's spam rate = %v, want 0 — pooling would report %v", r, 30.0/70.0)
	}
	if r := rateOf(t, dirty, "spam", dirty.SpamRate); !closeTo(r, 1) {
		t.Errorf("spam-only version's spam rate = %v, want 1 — pooling would report %v", r, 30.0/70.0)
	}
	if r := rateOf(t, clean, "inbox", clean.InboxRate); !closeTo(r, 1) {
		t.Errorf("clean version's inbox rate = %v, want 1", r)
	}
	if r := rateOf(t, dirty, "inbox", dirty.InboxRate); !closeTo(r, 0) {
		t.Errorf("spam-only version's inbox rate = %v, want 0", r)
	}
}

// A version below the floor reports its COUNTS and no rates. Splitting a shared
// library across a pool makes small cells the normal case, not the exception, so this
// is the branch most rows will take.
func TestFoldReportsNotEstablishedBelowTheSampleFloor(t *testing.T) {
	below := MinPlacementSamples - 1
	got := FoldContentVersions([]ContentVersionStat{
		{Version: "sl1:cccccccccccccccc", Inbox: below - 3, Spam: 3},
	})
	if len(got) != 1 {
		t.Fatalf("folded %d versions, want 1", len(got))
	}
	p := got[0]
	if p.PlacementSample != below {
		t.Fatalf("sample = %d, want %d", p.PlacementSample, below)
	}
	if p.InboxRate != nil || p.SpamRate != nil {
		t.Errorf("a %d-sample version reported rates (inbox=%v spam=%v); %d is the floor below which "+
			"a rate is noise dressed as a measurement", below, p.InboxRate, p.SpamRate, MinPlacementSamples)
	}
	if p.Inbox != below-3 || p.Spam != 3 {
		t.Errorf("counts came back %d/%d, want %d/%d: not-established withholds the RATES, not the "+
			"evidence behind them", p.Inbox, p.Spam, below-3, 3)
	}
}

// Exactly at the floor is established — the boundary the four previous applications
// of this rule all had to pin.
func TestFoldEstablishesAVersionExactlyAtTheFloor(t *testing.T) {
	got := FoldContentVersions([]ContentVersionStat{
		{Version: "sl1:dddddddddddddddd", Inbox: MinPlacementSamples - 2, Spam: 2},
	})
	p := got[0]
	if p.InboxRate == nil || p.SpamRate == nil {
		t.Fatalf("a version with exactly %d samples reported no rates; the floor is a minimum, "+
			"not a threshold to exceed", MinPlacementSamples)
	}
	if r := *p.SpamRate; !closeTo(r, 2.0/float64(MinPlacementSamples)) {
		t.Errorf("spam rate = %v, want %v", r, 2.0/float64(MinPlacementSamples))
	}
}

// A version whose placements were all 'other' has real observations and a scoreable
// sample of zero. Reporting a 0% spam rate for it would claim a clean measurement
// nobody made; it must read as not established, and must not divide by zero.
func TestFoldReportsNoRateForAVersionWithNothingScoreable(t *testing.T) {
	got := FoldContentVersions([]ContentVersionStat{
		{Version: "sl1:eeeeeeeeeeeeeeee", Inbox: 0, Spam: 0},
	})
	p := got[0]
	if p.PlacementSample != 0 {
		t.Fatalf("sample = %d, want 0", p.PlacementSample)
	}
	if p.InboxRate != nil || p.SpamRate != nil {
		t.Errorf("a version with no scoreable placement reported rates (inbox=%v spam=%v) — "+
			"that is a 0%% spam rate invented out of an empty window", p.InboxRate, p.SpamRate)
	}
}

// Observations recorded before this slice carry the empty version. They are a real
// bucket and must be reported as one rather than silently dropped or merged into a
// real template's rate — an operator has to be able to see how much of the window is
// unattributed.
func TestFoldKeepsUnattributedObservationsAsTheirOwnBucket(t *testing.T) {
	got := FoldContentVersions([]ContentVersionStat{
		{Version: "", Inbox: 90, Spam: 10},
		{Version: "sl1:ffffffffffffffff", Inbox: 18, Spam: 2},
	})
	if len(got) != 2 {
		t.Fatalf("folded %d versions, want 2 — the unattributed bucket was dropped or merged", len(got))
	}
	var unattributed *ContentVersionPlacement
	for i := range got {
		if got[i].Version == "" {
			unattributed = &got[i]
		}
	}
	if unattributed == nil {
		t.Fatal("no bucket for the empty version")
	}
	if unattributed.PlacementSample != 100 {
		t.Errorf("unattributed sample = %d, want 100", unattributed.PlacementSample)
	}
	for _, p := range got {
		if p.Version == "" {
			continue
		}
		if p.PlacementSample != 20 {
			t.Errorf("version %q has sample %d, want 20 — the unattributed bucket's 100 observations "+
				"leaked into a real template's denominator", p.Version, p.PlacementSample)
		}
	}
}

func TestFoldOfNothingIsEmpty(t *testing.T) {
	if got := FoldContentVersions(nil); len(got) != 0 {
		t.Errorf("FoldContentVersions(nil) returned %d rows, want 0", len(got))
	}
}
