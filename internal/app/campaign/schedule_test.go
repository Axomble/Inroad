package campaign

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cadence"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func TestGetScheduleReturnsTimezoneAndWindows(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, Timezone: "America/New_York"}},
		windows:   []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}},
	}
	svc := NewService(store, okChecker{active: true})

	got, err := svc.GetSchedule(context.Background(), ws, id)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if got.Timezone != "America/New_York" {
		t.Errorf("timezone = %q, want America/New_York", got.Timezone)
	}
	if len(got.Windows) != 1 || got.Windows[0].StartMinute != 540 {
		t.Errorf("windows = %+v, want one 540-1020 interval", got.Windows)
	}
}

func TestGetScheduleCrossTenantIsNotFound(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}}}
	svc := NewService(store, okChecker{active: true})

	if _, err := svc.GetSchedule(context.Background(), uuid.New(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSetScheduleRejectsInvalidSchedulesWithoutWriting(t *testing.T) {
	cases := []struct {
		name string
		in   Schedule
		want error
	}{
		{
			name: "unknown timezone",
			in:   Schedule{Timezone: "Mars/Olympus_Mons", Windows: []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}}},
			want: cadence.ErrUnknownTimezone,
		},
		{
			name: "nothing open all week",
			in:   Schedule{Timezone: "UTC"},
			want: cadence.ErrEmptySchedule,
		},
		{
			name: "inverted interval",
			in:   Schedule{Timezone: "UTC", Windows: []SendWindow{{Weekday: 1, StartMinute: 1020, EndMinute: 540}}},
			want: cadence.ErrBadSchedule,
		},
		{
			name: "overlapping intervals on one day",
			in: Schedule{Timezone: "UTC", Windows: []SendWindow{
				{Weekday: 1, StartMinute: 540, EndMinute: 720},
				{Weekday: 1, StartMinute: 700, EndMinute: 900},
			}},
			want: cadence.ErrBadSchedule,
		},
		{
			name: "weekday out of range",
			in:   Schedule{Timezone: "UTC", Windows: []SendWindow{{Weekday: 9, StartMinute: 540, EndMinute: 1020}}},
			want: cadence.ErrBadSchedule,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, id := uuid.New(), uuid.New()
			store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}}}
			svc := NewService(store, okChecker{active: true})

			if _, err := svc.SetSchedule(context.Background(), ws, id, tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			// Validation must precede persistence: a rejected schedule leaves the
			// previous one untouched.
			if store.replacedSchedule != nil {
				t.Errorf("invalid schedule was persisted: %+v", *store.replacedSchedule)
			}
		})
	}
}

func TestSetScheduleDefaultsBlankTimezoneToUTC(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}}}
	svc := NewService(store, okChecker{active: true})

	got, err := svc.SetSchedule(context.Background(), ws, id, Schedule{
		Windows: []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}},
	})
	if err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	if got.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", got.Timezone)
	}
	if store.replacedSchedule == nil || store.replacedSchedule.Timezone != "UTC" {
		t.Errorf("persisted schedule = %+v, want timezone UTC", store.replacedSchedule)
	}
}

func TestSetSchedulePropagatesStoreFailure(t *testing.T) {
	ws, id := uuid.New(), uuid.New()
	boom := errors.New("constraint violation")
	store := &fakeStore{
		campaigns:          map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id}},
		replaceScheduleErr: boom,
	}
	svc := NewService(store, okChecker{active: true})

	if _, err := svc.SetSchedule(context.Background(), ws, id, DefaultSchedule("UTC")); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store error", err)
	}
}

// The regression this whole feature exists for: a launch must not put every send
// on a uniform grid, and every send must land inside the campaign's window.
func TestLaunchSpreadsSendsAcrossTheWindowOffTheGrid(t *testing.T) {
	const n = 120
	enrollments := make([]Enrollment, n)
	for i := range enrollments {
		enrollments[i] = Enrollment{ID: uuid.New()}
	}
	store := &fakeStore{status: string(StatusDraft), steps: 1, enrollments: enrollments}
	svc := NewService(store, okChecker{active: true})

	if _, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{}); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if len(store.rescheduled) != n {
		t.Fatalf("stamped %d due times, want %d", len(store.rescheduled), n)
	}

	win, err := DefaultSchedule("UTC").Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	gaps := map[time.Duration]int{}
	times := make([]time.Time, 0, n)
	for _, at := range store.rescheduled {
		if !win.Contains(at) {
			t.Fatalf("send scheduled at %s, outside the Mon–Fri 09:00–17:00 window", at)
		}
		if at.Second() == 0 && at.Nanosecond() == 0 {
			t.Fatalf("send scheduled at %s, exactly on a clock boundary", at)
		}
		times = append(times, at)
	}
	// Distinct instants: a grid of identical times would collapse this.
	seen := map[time.Time]bool{}
	for _, at := range times {
		if seen[at] {
			t.Fatalf("duplicate send instant %s", at)
		}
		seen[at] = true
	}
	// Gaps must vary. Truncating to the minute keeps the assertion about the
	// spread rather than the sub-minute humanization.
	sortTimes(times)
	for i := 1; i < len(times); i++ {
		gaps[times[i].Truncate(time.Minute).Sub(times[i-1].Truncate(time.Minute))]++
	}
	if len(gaps) < 3 {
		t.Errorf("only %d distinct inter-send gaps across %d sends; the spread is still a grid", len(gaps), n)
	}
}

// A schedule that cannot be loaded or compiled must fail the launch loudly.
// Falling back to "send now" would put the whole list outside the operator's
// window, which is precisely what this feature prevents.
func TestLaunchSurfacesAnUnusableSchedule(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fakeStore)
	}{
		{
			name:   "windows cannot be read",
			mutate: func(f *fakeStore) { f.windowsErr = errors.New("windows unavailable") },
		},
		{
			name: "campaign timezone is not a known IANA zone",
			mutate: func(f *fakeStore) {
				f.campaignTimezone = "Mars/Olympus_Mons"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{
				status: string(StatusDraft), steps: 1,
				enrollments: []Enrollment{{ID: uuid.New()}},
			}
			tc.mutate(store)
			enq := &fakeEnqueuer{}
			svc := NewService(store, okChecker{active: true})

			if _, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq); err == nil {
				t.Fatal("Launch succeeded despite an unusable schedule")
			}
			if len(store.rescheduled) != 0 {
				t.Errorf("due times were stamped despite the failure: %v", store.rescheduled)
			}
			if len(enq.enqueued) != 0 {
				t.Errorf("enqueued %d advances despite the failure", len(enq.enqueued))
			}
		})
	}
}

func TestLaunchSurfacesRescheduleFailure(t *testing.T) {
	boom := errors.New("batch update failed")
	store := &fakeStore{
		status: string(StatusDraft), steps: 1,
		enrollments:   []Enrollment{{ID: uuid.New()}},
		rescheduleErr: boom,
	}
	enq := &fakeEnqueuer{}
	svc := NewService(store, okChecker{active: true})

	if _, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), enq); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store error", err)
	}
	// Nothing may be enqueued against due times that were never persisted, or the
	// task and the enrollment cursor would disagree.
	if len(enq.enqueued) != 0 {
		t.Errorf("enqueued %d advances after the stamp failed", len(enq.enqueued))
	}
}

func TestLaunchReportsTheLastScheduledInstant(t *testing.T) {
	enrollments := make([]Enrollment, 40)
	for i := range enrollments {
		enrollments[i] = Enrollment{ID: uuid.New()}
	}
	store := &fakeStore{status: string(StatusDraft), steps: 1, enrollments: enrollments}
	svc := NewService(store, okChecker{active: true})

	res, err := svc.Launch(context.Background(), uuid.New(), uuid.New(), &fakeEnqueuer{})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	var want time.Time
	for _, at := range store.rescheduled {
		if at.After(want) {
			want = at
		}
	}
	if !res.LastScheduledAt.Equal(want) {
		t.Errorf("LastScheduledAt = %s, want the latest stamped %s", res.LastScheduledAt, want)
	}
}

func TestDefaultScheduleIsWeekdayBusinessHours(t *testing.T) {
	got := DefaultSchedule("")
	if got.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", got.Timezone)
	}
	if len(got.Windows) != 5 {
		t.Fatalf("windows = %d, want 5 weekdays", len(got.Windows))
	}
	for _, w := range got.Windows {
		if w.Weekday == int(time.Saturday) || w.Weekday == int(time.Sunday) {
			t.Errorf("default schedule includes weekend day %d", w.Weekday)
		}
		if w.StartMinute != 9*60 || w.EndMinute != 17*60 {
			t.Errorf("window = %d-%d, want 540-1020", w.StartMinute, w.EndMinute)
		}
	}
	if _, err := got.Compile(); err != nil {
		t.Errorf("the default schedule does not compile: %v", err)
	}
}

// sortTimes sorts ascending; times come out of a map in arbitrary order.
func sortTimes(ts []time.Time) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].Before(ts[j-1]); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}
