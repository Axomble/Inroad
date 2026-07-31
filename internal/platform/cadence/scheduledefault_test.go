package cadence

import (
	"errors"
	"testing"
	"time"
)

// The defect this guards: a campaign created by any path that doesn't seed window
// rows (a direct INSERT, the seeder, an importer) used to compile to
// ErrNoOpenInterval, which failed every send it would ever make. Zero rows means
// "never configured" — no write path can persist an empty week — so it resolves to
// the default instead.
func TestScheduleFromDefaultsWhenNoWindowsArePersisted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		windows []SendWindow
	}{
		{"nil windows", nil},
		{"empty windows", []SendWindow{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ScheduleFrom("America/New_York", tc.windows)
			if got.Timezone != "America/New_York" {
				t.Errorf("timezone = %q, want the campaign's own zone", got.Timezone)
			}
			if len(got.Windows) != 5 {
				t.Fatalf("windows = %d, want the 5-weekday default", len(got.Windows))
			}
			win, err := got.Compile()
			if err != nil {
				t.Fatalf("an unconfigured schedule must still compile, got %v", err)
			}
			// And it must actually be usable for scheduling.
			if _, err := win.Next(time.Date(2026, 8, 3, 3, 0, 0, 0, time.UTC), "k"); err != nil {
				t.Errorf("Next on a defaulted schedule: %v", err)
			}
		})
	}
}

func TestScheduleFromKeepsConfiguredWindows(t *testing.T) {
	windows := []SendWindow{{Weekday: 2, StartMinute: 600, EndMinute: 900}}
	got := ScheduleFrom("UTC", windows)
	if len(got.Windows) != 1 || got.Windows[0].Weekday != 2 {
		t.Errorf("windows = %+v, want the configured one untouched", got.Windows)
	}
}

func TestScheduleFromDefaultsBlankTimezoneToUTC(t *testing.T) {
	got := ScheduleFrom("", []SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}})
	if got.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", got.Timezone)
	}
}

// An unknown zone must still fail: it means a binary without tzdata or a zone
// dropped from the IANA database, and guessing would send outside the window.
func TestCompileStillRejectsAnUnknownTimezone(t *testing.T) {
	_, err := ScheduleFrom("Mars/Olympus_Mons", nil).Compile()
	if !errors.Is(err, ErrUnknownTimezone) {
		t.Fatalf("err = %v, want ErrUnknownTimezone", err)
	}
}
