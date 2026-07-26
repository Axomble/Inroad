package warmup

import (
	"testing"
	"time"
)

func TestRampTarget(t *testing.T) {
	cases := []struct {
		name                                     string
		startVol, maxVol, increment, daysWarming int
		want                                     int
	}{
		{"day zero is start", 4, 40, 2, 0, 4},
		{"linear ramp", 4, 40, 2, 5, 14},
		{"caps at max", 4, 40, 2, 100, 40},
		{"exact cap boundary", 4, 40, 2, 18, 40},
		{"negative days clamp to start", 4, 40, 2, -3, 4},
		{"zero increment stays at start", 4, 40, 0, 10, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RampTarget(tc.startVol, tc.maxVol, tc.increment, tc.daysWarming); got != tc.want {
				t.Fatalf("RampTarget = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDailyVolumeFactorRangeAndDeterminism(t *testing.T) {
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	for _, mbx := range []string{"a", "b", "mailbox-xyz", ""} {
		for d := 0; d < 30; d++ {
			at := day.AddDate(0, 0, d)
			f := DailyVolumeFactor(mbx, at)
			if f < 0.8 || f > 1.1 {
				t.Fatalf("factor %f out of [0.8,1.1] for %q on %s", f, mbx, at)
			}
			if again := DailyVolumeFactor(mbx, at); again != f {
				t.Fatalf("non-deterministic: %f vs %f", f, again)
			}
		}
	}
}

func TestNextSpacingRangeAndZero(t *testing.T) {
	if got := NextSpacing(0, "m", 0); got != 0 {
		t.Fatalf("target 0 should give 0 spacing, got %s", got)
	}
	if got := NextSpacing(-5, "m", 0); got != 0 {
		t.Fatalf("negative target should give 0 spacing, got %s", got)
	}

	window := time.Duration(wakingEndHour-wakingStartHour) * time.Hour
	for _, target := range []int{1, 8, 40} {
		base := window / time.Duration(target)
		lo := time.Duration(float64(base) * 0.6)
		hi := time.Duration(float64(base) * 1.4)
		for idx := 0; idx < 50; idx++ {
			got := NextSpacing(target, "mbx", idx)
			if got < lo || got >= hi {
				t.Fatalf("spacing %s outside [%s,%s) for target=%d idx=%d", got, lo, hi, target, idx)
			}
			if again := NextSpacing(target, "mbx", idx); again != got {
				t.Fatalf("non-deterministic spacing: %s vs %s", got, again)
			}
		}
	}
}

func TestEngageDwellBoundsAndDeterminism(t *testing.T) {
	lo := 5 * time.Second
	hi := time.Hour
	for _, id := range []string{"r1", "r2", "receipt-abc", ""} {
		d := EngageDwell(id)
		if d < lo || d > hi {
			t.Fatalf("dwell %s outside [%s,%s] for %q", d, lo, hi, id)
		}
		if again := EngageDwell(id); again != d {
			t.Fatalf("non-deterministic dwell: %s vs %s", d, again)
		}
	}
}

func TestDeferToWakingHours(t *testing.T) {
	mk := func(h, m int) time.Time {
		return time.Date(2026, 1, 15, h, m, 0, 0, time.UTC)
	}
	cases := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{"exactly 07:00 is waking", mk(7, 0), mk(7, 0)},
		{"midday unchanged", mk(12, 0), mk(12, 0)},
		{"21:59 still waking", mk(21, 59), mk(21, 59)},
		{"exactly 22:00 defers to next morning", mk(22, 0), mk(7, 0).AddDate(0, 0, 1)},
		{"late night defers to next morning", mk(23, 30), mk(7, 0).AddDate(0, 0, 1)},
		{"midnight defers to same morning", mk(0, 0), mk(7, 0)},
		{"06:59 defers to same morning", mk(6, 59), mk(7, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeferToWakingHours(tc.in, time.UTC); !got.Equal(tc.want) {
				t.Fatalf("DeferToWakingHours(%s) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}

	// nil location defaults to UTC and must not panic.
	if got := DeferToWakingHours(mk(2, 0), nil); !got.Equal(mk(7, 0)) {
		t.Fatalf("nil loc: got %s want %s", got, mk(7, 0))
	}
}

func TestHealthStateTransitions(t *testing.T) {
	cases := []struct {
		name          string
		spam, bounce  float64
		invalidTokens int
		current       string
		wantState     string
	}{
		{"clean stays healthy", 0.05, 0.0, 0, StateHealthy, StateHealthy},
		{"spam over 15 -> watch", 0.20, 0.0, 0, StateHealthy, StateWatch},
		{"spam over 30 -> throttled", 0.35, 0.0, 0, StateHealthy, StateThrottled},
		{"spam over 50 -> paused", 0.60, 0.0, 0, StateHealthy, StatePaused},
		{"bounce spike -> paused", 0.0, 0.20, 0, StateHealthy, StatePaused},
		{"invalid tokens -> throttled", 0.0, 0.0, 3, StateHealthy, StateThrottled},
		{"escalation is immediate", 0.60, 0.0, 0, StateWatch, StatePaused},
		{"recover paused -> throttled on clean", 0.0, 0.0, 0, StatePaused, StateThrottled},
		{"recover throttled -> watch on clean", 0.0, 0.0, 0, StateThrottled, StateWatch},
		{"recover watch -> healthy on clean", 0.0, 0.0, 0, StateWatch, StateHealthy},
		{"holds level when signals match state", 0.20, 0.0, 0, StateWatch, StateWatch},
		{"unknown current treated as healthy", 0.20, 0.0, 0, "bogus", StateWatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := HealthState(tc.spam, tc.bounce, tc.invalidTokens, tc.current)
			if got != tc.wantState {
				t.Fatalf("state = %q, want %q (reason %q)", got, tc.wantState, reason)
			}
			if got != StateHealthy && reason == "" {
				t.Fatalf("expected a non-empty reason for non-healthy state %q", got)
			}
			if got == StateHealthy && reason != "" {
				t.Fatalf("expected empty reason for healthy, got %q", reason)
			}
		})
	}
}
