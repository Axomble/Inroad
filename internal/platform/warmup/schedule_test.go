package warmup

import (
	"sort"
	"strconv"
	"strings"
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

// TestDailyVolumeFactorRangeAndDeterminism pins the factor's overall bounds and its
// determinism in (mailbox, calendar day). The range is now [0, 1.1], NOT the old
// [0.8, 1.1]: a weekend scales the day down hard and a skipped weekday returns 0.
func TestDailyVolumeFactorRangeAndDeterminism(t *testing.T) {
	day := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	for _, mbx := range []string{"a", "b", "mailbox-xyz", ""} {
		for d := 0; d < 400; d++ {
			at := day.AddDate(0, 0, d)
			f := DailyVolumeFactor(mbx, at)
			if f < 0 || f > 1.1 {
				t.Fatalf("factor %f out of [0,1.1] for %q on %s", f, mbx, at)
			}
			if again := DailyVolumeFactor(mbx, at); again != f {
				t.Fatalf("non-deterministic: %f vs %f", f, again)
			}
		}
	}
}

// TestDailyVolumeFactorWeekendsAreQuieter proves the coarse weekend shape: every
// Saturday and Sunday scales well below the weekday floor of 0.8, and the average
// weekend day is a small fraction of the average weekday. Asserted as a shape over a
// long span, not as exact per-day values, which would be brittle.
func TestDailyVolumeFactorWeekendsAreQuieter(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) // a Monday
	var weekendSum, weekdaySum float64
	var weekendN, weekdayN int

	for d := 0; d < 364; d++ {
		at := start.AddDate(0, 0, d)
		f := DailyVolumeFactor("mbx-weekend", at)
		if wd := at.Weekday(); wd == time.Saturday || wd == time.Sunday {
			if f >= 0.8 {
				t.Fatalf("weekend %s factor %f, want well below the 0.8 weekday floor", at.Format("2006-01-02"), f)
			}
			weekendSum += f
			weekendN++
			continue
		}
		weekdaySum += f
		weekdayN++
	}

	weekendAvg := weekendSum / float64(weekendN)
	weekdayAvg := weekdaySum / float64(weekdayN)
	if weekendAvg >= weekdayAvg/2 {
		t.Errorf("weekend average %f is not materially below weekday average %f", weekendAvg, weekdayAvg)
	}
}

// TestDailyVolumeFactorSkipsSomeWeekdays proves weekdays are occasionally skipped
// outright (factor 0) but that skipping stays rare — a mailbox that goes quiet most
// days would never warm up. Bounds are deliberately loose around skipDayRate.
func TestDailyVolumeFactorSkipsSomeWeekdays(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) // a Monday
	skipped, weekdays := 0, 0
	for m := 0; m < 40; m++ {
		mbx := "mbx-" + strconv.Itoa(m)
		for d := 0; d < 120; d++ {
			at := start.AddDate(0, 0, d)
			if wd := at.Weekday(); wd == time.Saturday || wd == time.Sunday {
				continue
			}
			weekdays++
			if DailyVolumeFactor(mbx, at) == 0 {
				skipped++
			}
		}
	}
	rate := float64(skipped) / float64(weekdays)
	if rate <= 0 {
		t.Fatalf("no weekday was ever skipped across %d weekdays — coarse variation is absent", weekdays)
	}
	if rate > 3*skipDayRate {
		t.Fatalf("skip rate %.3f far above the configured %.3f — mailboxes would stall", rate, skipDayRate)
	}
}

// TestEffectiveDailyVolumeAntiStallFloor is the guard that keeps warmup progressing:
// a mailbox still early in its ramp always sends at least one email, on EVERY calendar
// day including weekends and would-be skip days. A larger target is allowed to reach a
// genuine zero-send day.
func TestEffectiveDailyVolumeAntiStallFloor(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

	for _, target := range []int{1, 4, skipDayFloorTarget} {
		for m := 0; m < 20; m++ {
			mbx := "low-" + strconv.Itoa(m)
			for d := 0; d < 120; d++ {
				at := start.AddDate(0, 0, d)
				if got := EffectiveDailyVolume(target, mbx, at); got < 1 {
					t.Fatalf("target %d, mailbox %q on %s: effective volume %d — an early-ramp mailbox must never be zeroed out",
						target, mbx, at.Format("2006-01-02"), got)
				}
			}
		}
	}

	// A non-positive target has nothing to send regardless of the day's shape.
	if got := EffectiveDailyVolume(0, "m", start); got != 0 {
		t.Errorf("target 0: effective volume = %d, want 0", got)
	}
	if got := EffectiveDailyVolume(-5, "m", start); got != 0 {
		t.Errorf("negative target: effective volume = %d, want 0", got)
	}

	// A high-volume mailbox DOES get the occasional true zero day — that is the point
	// of the coarse variation, and the floor deliberately does not apply up here.
	sawZero := false
	for m := 0; m < 40 && !sawZero; m++ {
		mbx := "high-" + strconv.Itoa(m)
		for d := 0; d < 200; d++ {
			if EffectiveDailyVolume(80, mbx, start.AddDate(0, 0, d)) == 0 {
				sawZero = true
				break
			}
		}
	}
	if !sawZero {
		t.Error("no high-volume mailbox ever had a zero-send day — coarse day variation is not reaching the quota")
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

// TestReplyDelayBoundsAndDeterminism pins the reply distribution's hard bounds and its
// determinism in the receipt id.
func TestReplyDelayBoundsAndDeterminism(t *testing.T) {
	lo := 3 * time.Minute
	hi := 8 * time.Hour
	for i := 0; i < 500; i++ {
		id := "receipt-" + strconv.Itoa(i)
		d := ReplyDelay(id)
		if d < lo || d > hi {
			t.Fatalf("reply delay %s outside [%s,%s] for %q", d, lo, hi, id)
		}
		if again := ReplyDelay(id); again != d {
			t.Fatalf("non-deterministic reply delay: %s vs %s", d, again)
		}
	}
}

// TestReplyDelayIsFarSlowerThanPassiveDwell is the regression test for the "traffic
// feels instant" report: replies must be an order of magnitude slower than passive
// engagement, not a slightly widened version of it. Asserted as distribution shape
// (medians, a floor, and a strong majority) rather than exact values.
func TestReplyDelayIsFarSlowerThanPassiveDwell(t *testing.T) {
	const n = 2000
	dwells := make([]time.Duration, 0, n)
	replies := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		id := "receipt-" + strconv.Itoa(i)
		dwells = append(dwells, EngageDwell(id))
		replies = append(replies, ReplyDelay(id))
	}

	// Every reply clears the floor, so no reply can ever land in the sub-minute range
	// where most passive dwells live.
	for i, d := range replies {
		if d < 3*time.Minute {
			t.Fatalf("reply %d delayed only %s — below the 3-minute human floor", i, d)
		}
	}

	medDwell := median(dwells)
	medReply := median(replies)
	if medReply < 20*time.Minute {
		t.Errorf("median reply delay %s, want at least 20 minutes", medReply)
	}
	if medReply < 10*medDwell {
		t.Errorf("median reply delay %s is not an order of magnitude above the median passive dwell %s",
			medReply, medDwell)
	}

	// "Mostly far above the passive range": the passive clamp tops out at 1h, so most
	// replies should exceed that entirely.
	over := 0
	for _, d := range replies {
		if d > time.Hour {
			over++
		}
	}
	if frac := float64(over) / float64(n); frac < 0.25 {
		t.Errorf("only %.0f%% of replies exceed the 1h passive ceiling, want a substantial tail", frac*100)
	}

	// Independent draws: a receipt's dwell must not determine its reply delay.
	identical := 0
	for i := range dwells {
		if dwells[i] == replies[i] {
			identical++
		}
	}
	if identical > 0 {
		t.Errorf("%d receipts had dwell == reply delay — the two draws are not independent", identical)
	}
}

// TestReplyEngageAfterAlwaysLandsInWakingHours is the invariant that a long
// heavy-tailed draw can never schedule a 03:00 reply: whatever hour the message
// arrived and however long the draw, now+delay is inside [07:00, 22:00).
func TestReplyEngageAfterAlwaysLandsInWakingHours(t *testing.T) {
	base := time.Date(2026, 3, 9, 0, 0, 0, 0, time.UTC) // a Monday
	for hour := 0; hour < 24; hour++ {
		for i := 0; i < 120; i++ {
			receivedAt := base.Add(time.Duration(hour) * time.Hour)
			id := "receipt-wake-" + strconv.Itoa(hour) + "-" + strconv.Itoa(i)

			d := ReplyEngageAfter(id, receivedAt, receivedAt, nil)
			if d < 0 {
				t.Fatalf("negative delay %s for %q", d, id)
			}
			at := receivedAt.Add(d).UTC()
			if h := at.Hour(); h < wakingStartHour || h >= wakingEndHour {
				t.Fatalf("reply for %q received %s would fire at %s (hour %d) — outside waking hours",
					id, receivedAt.Format(time.RFC3339), at.Format(time.RFC3339), h)
			}
			if again := ReplyEngageAfter(id, receivedAt, receivedAt, nil); again != d {
				t.Fatalf("non-deterministic ReplyEngageAfter: %s vs %s", d, again)
			}
		}
	}
}

// TestReplyEngageAfterStalePlanFiresAtNextWakingInstant covers the self-healing
// re-plan of a duplicate receipt discovered long after the fact: the original target
// has passed, so the reply is due at the next waking instant — immediately if we are
// already inside the window, never negative, and never at 03:00.
func TestReplyEngageAfterStalePlanFiresAtNextWakingInstant(t *testing.T) {
	receivedAt := time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC)

	// Inside waking hours, a day later: act now.
	nowWaking := receivedAt.AddDate(0, 0, 1)
	if got := ReplyEngageAfter("stale-1", receivedAt, nowWaking, nil); got != 0 {
		t.Errorf("stale plan inside waking hours: delay = %s, want 0", got)
	}

	// Outside waking hours: wait for 07:00 rather than replying at 03:00.
	nowNight := time.Date(2026, 3, 10, 3, 0, 0, 0, time.UTC)
	got := ReplyEngageAfter("stale-2", receivedAt, nowNight, nil)
	if got != 4*time.Hour {
		t.Errorf("stale plan at 03:00: delay = %s, want 4h (to 07:00)", got)
	}
	if h := nowNight.Add(got).Hour(); h != wakingStartHour {
		t.Errorf("stale plan fires at hour %d, want %d", h, wakingStartHour)
	}
}

// median returns the middle value of ds. It sorts a copy, leaving the caller's slice
// untouched.
func median(ds []time.Duration) time.Duration {
	sorted := make([]time.Duration, len(ds))
	copy(sorted, ds)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
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

func TestEvaluateHealthUsesQualifiedEvidence(t *testing.T) {
	cases := []struct {
		name        string
		in          HealthSignals
		state, code string
	}{
		{"no evidence is unknown", HealthSignals{Current: StateHealthy}, StateUnknown, "placement_sample_insufficient"},
		{"small bad sample is still unknown", HealthSignals{Current: StateHealthy, Spam: 19}, StateUnknown, "placement_sample_insufficient"},
		{"qualified spam reaches watch", HealthSignals{Current: StateHealthy, Inbox: 16, Spam: 4}, StateWatch, "spam_watch"},
		{"qualified bounce can pause", HealthSignals{Current: StateHealthy, BounceSamples: 50, Bounces: 6}, StatePaused, "bounce_pause"},
		{"qualified complaint can pause", HealthSignals{Current: StateHealthy, ComplaintSamples: 1000, Complaints: 4}, StatePaused, "complaint_pause"},
		{"degraded state cannot recover without placement", HealthSignals{Current: StatePaused}, StatePaused, "insufficient_evidence_to_recover"},
		{"clean qualified placement establishes health", HealthSignals{Current: StateUnknown, Inbox: 20}, StateHealthy, "evidence_qualified"},
		{"unknown escalates straight to the warranted state", HealthSignals{Current: StateUnknown, Inbox: 10, Spam: 10}, StateThrottled, "spam_throttle"},
		{"qualified recovery is gradual", HealthSignals{Current: StatePaused, Inbox: 20}, StateThrottled, "recovery_step"},
		{"trusted token failures throttle", HealthSignals{Current: StateHealthy, InvalidTokens: 3}, StateThrottled, "invalid_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateHealth(tc.in)
			if got.State != tc.state || got.ReasonCode != tc.code {
				t.Fatalf("decision = %q/%q, want %q/%q", got.State, got.ReasonCode, tc.state, tc.code)
			}
		})
	}
}

// Every transition the evaluator asks to persist must name why. warmup_state_
// transitions enforces reason_code <> ” and shares one atomic statement with the
// participant update, so an unexplained decision does not merely lose its audit
// trail — it aborts the state change itself and the mailbox never moves. This
// sweeps the whole decision surface rather than the one pair that regressed
// (unknown → healthy), because the empty code came from two states sharing a rank
// and any future state added to that scale can reintroduce it.
func TestEvaluateHealthExplainsEveryAppliedTransition(t *testing.T) {
	states := []string{StateUnknown, StateHealthy, StateWatch, StateThrottled, StatePaused}
	signals := []struct {
		name string
		in   HealthSignals
	}{
		{"no evidence", HealthSignals{}},
		{"clean qualified placement", HealthSignals{Inbox: 20}},
		{"spam at watch", HealthSignals{Inbox: 16, Spam: 4}},
		{"spam at throttle", HealthSignals{Inbox: 10, Spam: 10}},
		{"spam at pause", HealthSignals{Inbox: 4, Spam: 16}},
		{"qualified bounce", HealthSignals{BounceSamples: 50, Bounces: 6}},
		{"qualified complaint", HealthSignals{ComplaintSamples: 1000, Complaints: 4}},
		{"sub-threshold sample", HealthSignals{Inbox: 5, Spam: 14}},
	}
	var zero time.Time
	for _, from := range states {
		for _, sig := range signals {
			t.Run(from+"/"+sig.name, func(t *testing.T) {
				in := sig.in
				in.Current = from
				got := EvaluateHealth(in)
				if !ShouldApplyTransition(from, got.State, zero, zero) {
					return // no write, so nothing to explain
				}
				if got.ReasonCode == "" {
					t.Fatalf("%s -> %s would be persisted with an empty reason_code, which the transitions table rejects", from, got.State)
				}
				if got.Reason == "" {
					t.Fatalf("%s -> %s (%s) has no human-readable reason", from, got.State, got.ReasonCode)
				}
			})
		}
	}
}

func TestPairDailyCap(t *testing.T) {
	if got := PairDailyCap(10, 3); got != 4 {
		t.Fatalf("PairDailyCap(10, 3) = %d, want 4", got)
	}
	if PairDailyCap(10, 0) != 0 || PairDailyCap(0, 3) != 0 {
		t.Fatal("empty target or partner pool must produce a zero pair cap")
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
		{"partial recovery steps one level toward health", 0.20, 0.0, 0, StatePaused, StateThrottled},
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

// TestHealthStatePartialRecoveryReason pins that a step-down whose window is NOT
// clean — signals still warrant a non-healthy level, just a milder one — reports
// the persisting signal and never falsely claims a clean window. State is still a
// single step toward health (paused -> throttled); only the reason is at issue.
func TestHealthStatePartialRecoveryReason(t *testing.T) {
	got, reason := HealthState(0.20, 0.0, 0, StatePaused) // spam 0.20 warrants watch
	if got != StateThrottled {
		t.Fatalf("state = %q, want %q (reason %q)", got, StateThrottled, reason)
	}
	if strings.Contains(reason, "clean window") {
		t.Fatalf("reason must not claim a clean window while signals persist: %q", reason)
	}
	if !strings.Contains(reason, "15%") {
		t.Fatalf("reason should surface the still-present signal, got %q", reason)
	}
}
