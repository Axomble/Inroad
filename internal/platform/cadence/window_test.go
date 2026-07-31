package cadence

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

// businessHours is the default shape a campaign is created with: Mon–Fri
// 09:00–17:00, nothing at the weekend.
func businessHours(t *testing.T, loc *time.Location) Window {
	t.Helper()
	var days [7][]Interval
	for d := time.Monday; d <= time.Friday; d++ {
		days[d] = []Interval{{StartMin: 9 * 60, EndMin: 17 * 60}}
	}
	w, err := NewWindow(loc, days)
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	return w
}

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v — the binary needs embedded tzdata", name, err)
	}
	return loc
}

func TestNextSnapsForwardWithinTheWeek(t *testing.T) {
	loc := mustLoad(t, "America/New_York")
	w := businessHours(t, loc)

	cases := []struct {
		name      string
		from      time.Time
		wantYMD   string
		wantHour  int
		wantAtDay time.Weekday
	}{
		{
			name:      "before the window opens moves to this morning",
			from:      time.Date(2026, 8, 3, 6, 30, 0, 0, loc), // Monday 06:30
			wantYMD:   "2026-08-03",
			wantHour:  9,
			wantAtDay: time.Monday,
		},
		{
			name:      "after the window closes moves to the next open day",
			from:      time.Date(2026, 8, 3, 19, 0, 0, 0, loc), // Monday 19:00
			wantYMD:   "2026-08-04",
			wantHour:  9,
			wantAtDay: time.Tuesday,
		},
		{
			name:      "friday evening skips the weekend",
			from:      time.Date(2026, 8, 7, 18, 0, 0, 0, loc), // Friday 18:00
			wantYMD:   "2026-08-10",
			wantHour:  9,
			wantAtDay: time.Monday,
		},
		{
			name:      "saturday skips to monday",
			from:      time.Date(2026, 8, 8, 11, 0, 0, 0, loc), // Saturday
			wantYMD:   "2026-08-10",
			wantHour:  9,
			wantAtDay: time.Monday,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := w.Next(tc.from, "enrollment-1")
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			local := got.In(loc)
			if ymd := local.Format("2006-01-02"); ymd != tc.wantYMD {
				t.Errorf("date = %s, want %s", ymd, tc.wantYMD)
			}
			if local.Hour() != tc.wantHour {
				t.Errorf("hour = %d, want %d", local.Hour(), tc.wantHour)
			}
			if local.Weekday() != tc.wantAtDay {
				t.Errorf("weekday = %v, want %v", local.Weekday(), tc.wantAtDay)
			}
			if !got.After(tc.from) {
				t.Errorf("Next moved backward: %s is not after %s", got, tc.from)
			}
			if !w.Contains(got) {
				t.Errorf("Next returned %s, which is outside the window", local)
			}
		})
	}
}

func TestNextKeepsAnInstantAlreadyInsideTheWindow(t *testing.T) {
	loc := mustLoad(t, "America/New_York")
	w := businessHours(t, loc)
	from := time.Date(2026, 8, 3, 14, 22, 0, 0, loc) // Monday 14:22, mid-window

	got, err := w.Next(from, "enrollment-1")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	local := got.In(loc)
	if local.Hour() != 14 || local.Minute() != 22 {
		t.Errorf("in-window instant was moved to %02d:%02d, want 14:22 (humanized only)",
			local.Hour(), local.Minute())
	}
	if got.Sub(from) > time.Minute {
		t.Errorf("humanization shifted the instant by %s, want under a minute", got.Sub(from))
	}
}

// Invariant 3: no computed send time may land on a clock boundary.
func TestNextNeverLandsOnAClockBoundary(t *testing.T) {
	loc := mustLoad(t, "Europe/Berlin")
	w := businessHours(t, loc)
	base := time.Date(2026, 8, 3, 0, 0, 0, 0, loc)

	for i := range 2000 {
		from := base.Add(time.Duration(i) * 7 * time.Minute)
		got, err := w.Next(from, "enrollment-"+strconv.Itoa(i))
		if err != nil {
			t.Fatalf("Next(%s): %v", from, err)
		}
		if got.Second() == 0 && got.Nanosecond() == 0 {
			t.Fatalf("Next(%s) = %s, which lands exactly on a clock boundary", from, got)
		}
		if !w.Contains(got) {
			t.Fatalf("Next(%s) = %s, outside the window", from, got.In(loc))
		}
	}
}

// Invariant 2: humanization must never push an instant past a window edge, even
// when the target minute is the very last one in the interval.
func TestHumanizationStaysInsideTheWindowAtTheEdge(t *testing.T) {
	loc := time.UTC
	w, err := NewWindow(loc, [7][]Interval{
		time.Monday: {{StartMin: 9 * 60, EndMin: 9*60 + 30}}, // Mon 09:00–09:30 only
	})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	// 09:29 is the interval's last minute: a forward nudge would escape it.
	from := time.Date(2026, 8, 3, 9, 29, 0, 0, loc)

	got, err := w.Next(from, "edge")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !w.Contains(got) {
		t.Fatalf("Next = %s, outside the 09:00–09:30 window", got)
	}
	if got.Second() == 0 && got.Nanosecond() == 0 {
		t.Errorf("Next = %s, on a clock boundary", got)
	}
}

// Invariant 1: retry determinism. A retried task must recompute the identical
// instant, or next_due_at and the enqueued task time drift apart.
func TestNextIsDeterministic(t *testing.T) {
	loc := mustLoad(t, "America/New_York")
	w := businessHours(t, loc)
	from := time.Date(2026, 8, 3, 6, 0, 0, 0, loc)

	first, err := w.Next(from, "enrollment-42")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	for range 1000 {
		got, err := w.Next(from, "enrollment-42")
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !got.Equal(first) {
			t.Fatalf("Next is not deterministic: got %s, first call gave %s", got, first)
		}
	}
	// A different key must move the instant, or the jitter isn't keyed at all.
	other, err := w.Next(from, "enrollment-43")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if other.Equal(first) {
		t.Error("different keys produced the identical instant; humanization is not keyed")
	}
}

func TestNextAcrossDSTSpringForward(t *testing.T) {
	loc := mustLoad(t, "America/New_York")
	// 2026-03-08: 02:00 EST jumps to 03:00 EDT — 02:00–03:00 local does not exist.
	w, err := NewWindow(loc, [7][]Interval{
		time.Sunday: {{StartMin: 2 * 60, EndMin: 4 * 60}}, // 02:00–04:00 on the gap day
	})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	from := time.Date(2026, 3, 8, 0, 30, 0, 0, loc)

	got, err := w.Next(from, "dst")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !got.After(from) {
		t.Fatalf("Next = %s, not after %s", got, from)
	}
	// The nonexistent local hour must not produce an instant before the window,
	// and must resolve to a real moment inside 02:00–04:00 wall-clock terms.
	if !w.Contains(got) {
		t.Errorf("Next = %s (%s local), outside the DST-day window", got, got.In(loc))
	}
}

func TestNextAcrossDSTFallBack(t *testing.T) {
	loc := mustLoad(t, "America/New_York")
	// 2026-11-01: 02:00 EDT falls back to 01:00 EST — 01:00–02:00 happens twice.
	w, err := NewWindow(loc, [7][]Interval{
		time.Sunday: {{StartMin: 60, EndMin: 3 * 60}}, // 01:00–03:00 on the repeat day
	})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	from := time.Date(2026, 11, 1, 0, 15, 0, 0, loc)

	got, err := w.Next(from, "dst-fall")
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !got.After(from) {
		t.Errorf("Next = %s, not after %s", got, from)
	}
	if !w.Contains(got) {
		t.Errorf("Next = %s (%s local), outside the window", got, got.In(loc))
	}
}

func TestNextAfterOffsetRollsIntoLaterIntervalsAndDays(t *testing.T) {
	loc := time.UTC
	w := businessHours(t, loc)
	from := time.Date(2026, 8, 3, 9, 0, 0, 0, loc) // Monday, window open

	cases := []struct {
		name    string
		offset  time.Duration
		wantDay time.Weekday
		wantHr  int
	}{
		{"within the first day", 2 * time.Hour, time.Monday, 11},
		{"just inside the first day", 7*time.Hour + 30*time.Minute, time.Monday, 16},
		{"past the first day rolls to tuesday", 9 * time.Hour, time.Tuesday, 10},
		{"three open days out lands thursday", 25 * time.Hour, time.Thursday, 10},
		{"the last open hour of friday", 39 * time.Hour, time.Friday, 16},
		// Mon–Fri is 40h of open time, so anything past that rolls to next Monday.
		{"past friday close rolls over the weekend", 41 * time.Hour, time.Monday, 10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := w.NextAfterOffset(from, tc.offset, "batch")
			if err != nil {
				t.Fatalf("NextAfterOffset: %v", err)
			}
			local := got.In(loc)
			if local.Weekday() != tc.wantDay {
				t.Errorf("weekday = %v, want %v (got %s)", local.Weekday(), tc.wantDay, local)
			}
			if local.Hour() != tc.wantHr {
				t.Errorf("hour = %d, want %d (got %s)", local.Hour(), tc.wantHr, local)
			}
			if !w.Contains(got) {
				t.Errorf("%s is outside the window", local)
			}
		})
	}
}

func TestNextAfterOffsetIsMonotonicInOffset(t *testing.T) {
	loc := time.UTC
	w := businessHours(t, loc)
	from := time.Date(2026, 8, 3, 9, 0, 0, 0, loc)

	var prev time.Time
	for i := range 200 {
		off := time.Duration(i) * 11 * time.Minute
		got, err := w.NextAfterOffset(from, off, "batch")
		if err != nil {
			t.Fatalf("NextAfterOffset(%s): %v", off, err)
		}
		// Humanization can reorder two offsets inside the same minute; compare at
		// minute granularity, which is the resolution the offset addresses.
		if !prev.IsZero() && got.Truncate(time.Minute).Before(prev.Truncate(time.Minute)) {
			t.Fatalf("offset %s produced %s, earlier than the previous offset's %s", off, got, prev)
		}
		prev = got
	}
}

func TestValidateRejectsMalformedWindows(t *testing.T) {
	cases := []struct {
		name string
		days [7][]Interval
	}{
		{"inverted interval", [7][]Interval{time.Monday: {{StartMin: 600, EndMin: 540}}}},
		{"empty interval", [7][]Interval{time.Monday: {{StartMin: 600, EndMin: 600}}}},
		{"start out of range", [7][]Interval{time.Monday: {{StartMin: -1, EndMin: 600}}}},
		{"end past midnight", [7][]Interval{time.Monday: {{StartMin: 600, EndMin: 1441}}}},
		{"overlapping intervals", [7][]Interval{time.Monday: {
			{StartMin: 540, EndMin: 720}, {StartMin: 700, EndMin: 900},
		}}},
		{"no open interval at all", [7][]Interval{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWindow(time.UTC, tc.days); err == nil {
				t.Fatal("NewWindow accepted a malformed window")
			}
		})
	}
}

func TestAdjacentIntervalsAreNotAnOverlap(t *testing.T) {
	// Half-open [start, end) means 09:00–12:00 and 12:00–17:00 are adjacent.
	if _, err := NewWindow(time.UTC, [7][]Interval{time.Monday: {
		{StartMin: 540, EndMin: 720}, {StartMin: 720, EndMin: 1020},
	}}); err != nil {
		t.Fatalf("adjacent intervals rejected: %v", err)
	}
}

func TestNewWindowSortsAndDoesNotAliasInput(t *testing.T) {
	days := [7][]Interval{time.Monday: {
		{StartMin: 780, EndMin: 1020}, {StartMin: 540, EndMin: 720},
	}}
	w, err := NewWindow(time.UTC, days)
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if got := w.Days[time.Monday][0].StartMin; got != 540 {
		t.Errorf("intervals not sorted: first start = %d, want 540", got)
	}
	// Mutating the caller's slice must not reach into the window.
	days[time.Monday][0] = Interval{StartMin: 0, EndMin: 1}
	if got := w.Days[time.Monday][1].StartMin; got != 780 {
		t.Errorf("window aliases the caller's slice: got start %d, want 780", got)
	}
}

func TestAllClosedWindowReturnsErrorRatherThanLooping(t *testing.T) {
	// Bypass NewWindow's validation the way a corrupted persisted row would.
	w := Window{Loc: time.UTC}
	if _, err := w.Next(time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC), "k"); !errors.Is(err, ErrNoOpenInterval) {
		t.Fatalf("err = %v, want ErrNoOpenInterval", err)
	}
	if _, err := w.NextAfterOffset(time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC), time.Hour, "k"); !errors.Is(err, ErrNoOpenInterval) {
		t.Fatalf("err = %v, want ErrNoOpenInterval", err)
	}
}

func TestNextRequiresAKey(t *testing.T) {
	w := businessHours(t, time.UTC)
	if _, err := w.Next(time.Now(), ""); err == nil {
		t.Error("Next accepted an empty key; humanization would be unkeyed")
	}
	if _, err := w.NextAfterOffset(time.Now(), 0, ""); err == nil {
		t.Error("NextAfterOffset accepted an empty key")
	}
}

func TestOpenDuration(t *testing.T) {
	loc := time.UTC
	w, err := NewWindow(loc, [7][]Interval{
		time.Monday: {{StartMin: 540, EndMin: 720}, {StartMin: 780, EndMin: 1020}}, // 3h + 4h
	})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if got := w.OpenDuration(time.Date(2026, 8, 3, 12, 0, 0, 0, loc)); got != 7*time.Hour {
		t.Errorf("OpenDuration = %s, want 7h", got)
	}
	if got := w.OpenDuration(time.Date(2026, 8, 8, 12, 0, 0, 0, loc)); got != 0 {
		t.Errorf("closed day OpenDuration = %s, want 0", got)
	}
}

func TestNilLocationDefaultsToUTC(t *testing.T) {
	w, err := NewWindow(nil, [7][]Interval{time.Monday: {{StartMin: 540, EndMin: 1020}}})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if w.location() != time.UTC {
		t.Errorf("location = %v, want UTC", w.location())
	}
}
