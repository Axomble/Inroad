package cadence

import (
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestSpacingVariesAndStaysBounded(t *testing.T) {
	const (
		target = 40
		open   = 8 * time.Hour
	)
	base := open / target // 12m

	seen := make(map[time.Duration]int, target)
	for i := range target {
		got := Spacing(open, target, i, "mailbox-1")
		if got < time.Duration(float64(base)*jitterMin) || got > time.Duration(float64(base)*jitterMax) {
			t.Fatalf("Spacing(%d) = %s, outside the jitter band around %s", i, got, base)
		}
		seen[got]++
	}
	// The regression this exists for: the old scheduler produced one identical gap
	// for every send. Anything close to that is a fingerprint.
	if len(seen) < target*3/4 {
		t.Errorf("only %d distinct gaps across %d sends; spacing is too uniform", len(seen), target)
	}
	if _, collided := seen[base]; collided && len(seen) == 1 {
		t.Error("every gap equals the unjittered base spacing")
	}
}

func TestSpacingIsDeterministic(t *testing.T) {
	first := Spacing(8*time.Hour, 40, 7, "mailbox-1")
	for range 500 {
		if got := Spacing(8*time.Hour, 40, 7, "mailbox-1"); got != first {
			t.Fatalf("Spacing not deterministic: %s vs %s", got, first)
		}
	}
	if Spacing(8*time.Hour, 40, 7, "mailbox-2") == first {
		t.Error("different keys produced identical spacing; the jitter is not keyed")
	}
}

func TestSpacingHandlesDegenerateInput(t *testing.T) {
	cases := []struct {
		name   string
		open   time.Duration
		target int
	}{
		{"zero target", time.Hour, 0},
		{"negative target", time.Hour, -5},
		{"zero open time", 0, 10},
		{"negative open time", -time.Hour, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Spacing(tc.open, tc.target, 0, "k"); got != 0 {
				t.Errorf("Spacing = %s, want 0", got)
			}
		})
	}
}

func TestOffsetsAreAscendingAndBounded(t *testing.T) {
	const (
		n    = 250
		open = 8 * time.Hour
	)
	got := Offsets(open, n, "campaign-1")
	if len(got) != n {
		t.Fatalf("len = %d, want %d", len(got), n)
	}
	if !slices.IsSorted(got) {
		t.Error("offsets are not ascending")
	}
	for i, off := range got {
		if off < 0 || off >= open {
			t.Fatalf("offset %d = %s, outside [0, %s)", i, off, open)
		}
	}
}

func TestOffsetsAreNotUniformlySpaced(t *testing.T) {
	const (
		n    = 300
		open = 8 * time.Hour
	)
	got := Offsets(open, n, "campaign-1")

	// A uniform spread would give near-identical consecutive gaps. Assert the gaps
	// actually vary: max gap at least 2x the min gap.
	minGap, maxGap := time.Duration(1<<62), time.Duration(0)
	for i := 1; i < len(got); i++ {
		gap := got[i] - got[i-1]
		minGap = min(minGap, gap)
		maxGap = max(maxGap, gap)
	}
	if maxGap < 2*minGap {
		t.Errorf("gaps are near-uniform: min %s, max %s", minGap, maxGap)
	}
}

// The distribution curve should visibly front-load the day rather than spread
// flat, so the first half of the window carries more sends than the second.
func TestOffsetsFollowTheDistributionCurve(t *testing.T) {
	const (
		n    = 1000
		open = 8 * time.Hour
	)
	got := Offsets(open, n, "campaign-curve")

	var firstHalf int
	for _, off := range got {
		if off < open/2 {
			firstHalf++
		}
	}
	if firstHalf <= n/2 {
		t.Errorf("first half of the window carries %d/%d sends; the curve is not front-weighted",
			firstHalf, n)
	}
	// Sanity bound: front-weighted, not degenerate.
	if firstHalf > n*85/100 {
		t.Errorf("first half carries %d/%d sends; the curve is too extreme", firstHalf, n)
	}
}

func TestOffsetsAreDeterministic(t *testing.T) {
	first := Offsets(8*time.Hour, 50, "campaign-1")
	for range 200 {
		if got := Offsets(8*time.Hour, 50, "campaign-1"); !slices.Equal(got, first) {
			t.Fatal("Offsets is not deterministic")
		}
	}
	if other := Offsets(8*time.Hour, 50, "campaign-2"); slices.Equal(other, first) {
		t.Error("different keys produced identical offsets")
	}
}

func TestOffsetsHandlesDegenerateInput(t *testing.T) {
	if got := Offsets(time.Hour, 0, "k"); got != nil {
		t.Errorf("n=0 returned %v, want nil", got)
	}
	if got := Offsets(time.Hour, -3, "k"); got != nil {
		t.Errorf("negative n returned %v, want nil", got)
	}
	if got := Offsets(0, 10, "k"); got != nil {
		t.Errorf("zero open time returned %v, want nil", got)
	}
}

func TestOffsetsSingleSendFitsInTheWindow(t *testing.T) {
	got := Offsets(time.Hour, 1, "k")
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0] < 0 || got[0] >= time.Hour {
		t.Errorf("offset = %s, outside [0, 1h)", got[0])
	}
}

// End-to-end of the spread half: a launch's offsets resolved through the window
// must all land inside open intervals, off the clock grid, and in order.
func TestOffsetsResolveIntoRealInWindowInstants(t *testing.T) {
	loc := mustLoad(t, "America/New_York")
	w := businessHours(t, loc)
	from := time.Date(2026, 8, 3, 9, 0, 0, 0, loc) // Monday, window open

	const n = 200
	offsets := Offsets(w.OpenDuration(from), n, "campaign-1")

	var prev time.Time
	for i, off := range offsets {
		got, err := w.NextAfterOffset(from, off, "enrollment-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("NextAfterOffset(%s): %v", off, err)
		}
		if !w.Contains(got) {
			t.Fatalf("send %d at %s is outside the window", i, got.In(loc))
		}
		if got.Second() == 0 && got.Nanosecond() == 0 {
			t.Fatalf("send %d at %s lands on a clock boundary", i, got)
		}
		if !prev.IsZero() && got.Truncate(time.Minute).Before(prev.Truncate(time.Minute)) {
			t.Fatalf("send %d at %s precedes send %d at %s", i, got, i-1, prev)
		}
		prev = got
	}
}

func TestCurveCDFDegeneratesToUniform(t *testing.T) {
	cdf := curveCDF([]float64{0, 0, 0})
	want := []float64{0, 1.0 / 3, 2.0 / 3, 1}
	for i, w := range want {
		if diff := cdf[i] - w; diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("cdf[%d] = %f, want %f", i, cdf[i], w)
		}
	}
}

func TestInvCDFCoversTheUnitInterval(t *testing.T) {
	cdf := curveCDF(dayCurve)
	for i := range 101 {
		u := float64(i) / 100
		got := invCDF(cdf, u)
		if got < 0 || got > 1 {
			t.Fatalf("invCDF(%f) = %f, outside [0,1]", u, got)
		}
	}
	// Out-of-range input clamps rather than panicking or extrapolating.
	if got := invCDF(cdf, -1); got != 0 {
		t.Errorf("invCDF(-1) = %f, want 0", got)
	}
	if got := invCDF(cdf, 2); got != 1 {
		t.Errorf("invCDF(2) = %f, want 1", got)
	}
}

func TestScaleStaysInBand(t *testing.T) {
	for i := range 5000 {
		got := scale(hashU64("t", strconv.Itoa(i)), 0.55, 1.45)
		if got < 0.55 || got >= 1.45 {
			t.Fatalf("scale = %f, outside [0.55, 1.45)", got)
		}
	}
}
