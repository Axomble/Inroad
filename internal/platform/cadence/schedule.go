package cadence

import (
	"errors"
	"fmt"
	"time"
)

// Schedule errors. A schedule that would never send is a client mistake, not a
// server fault: it silently parks every enrollment, so it is rejected loudly.
var (
	ErrUnknownTimezone = errors.New("unknown IANA timezone")
	ErrEmptySchedule   = errors.New("schedule has no open interval in the week")
	ErrBadSchedule     = errors.New("invalid send window")
)

// defaultWindowStartMin / defaultWindowEndMin are the Mon–Fri business hours a
// campaign starts with. Migration 000031 backfills the identical default, so a
// campaign created before send windows existed and one created after are
// scheduled the same way.
const (
	defaultWindowStartMin = 9 * 60
	defaultWindowEndMin   = 17 * 60
)

// SendWindow is one open sending interval on one weekday, in minutes from local
// midnight, half-open [StartMinute, EndMinute). This is the transport/persistence
// shape of an Interval: flat, serializable, and carried across the coreapi seam.
type SendWindow struct {
	Weekday     int `json:"weekday"`
	StartMinute int `json:"start_minute"`
	EndMinute   int `json:"end_minute"`
}

// Schedule is a campaign's sending schedule as it is stored and transported: the
// IANA zone its windows are interpreted in, plus the week's open intervals.
// Compile turns it into the Window the engine schedules against.
type Schedule struct {
	Timezone string       `json:"timezone"`
	Windows  []SendWindow `json:"windows"`
}

// DefaultSchedule is Mon–Fri 09:00–17:00 in the given zone (UTC when empty).
func DefaultSchedule(tz string) Schedule {
	if tz == "" {
		tz = "UTC"
	}
	windows := make([]SendWindow, 0, 5)
	for d := int(time.Monday); d <= int(time.Friday); d++ {
		windows = append(windows, SendWindow{
			Weekday: d, StartMinute: defaultWindowStartMin, EndMinute: defaultWindowEndMin,
		})
	}
	return Schedule{Timezone: tz, Windows: windows}
}

// ScheduleFrom builds a Schedule from persisted rows, treating an EMPTY window
// set as "never configured" and substituting the default.
//
// This is the single boundary every reader goes through, and the reason it exists
// is that "campaign has window rows" cannot be enforced by the schema: a campaign
// created by anything other than the store's Create — a direct INSERT, the seeder,
// a future importer — would otherwise have no windows, and treating that as
// corrupted state would permanently break its sequence. Since a persisted
// schedule can never legitimately be empty (Compile rejects an all-closed week,
// so no write path can store one), zero rows unambiguously means "never
// configured", and defaulting is both safe and correct.
func ScheduleFrom(timezone string, windows []SendWindow) Schedule {
	if len(windows) == 0 {
		return DefaultSchedule(timezone)
	}
	if timezone == "" {
		timezone = "UTC"
	}
	return Schedule{Timezone: timezone, Windows: windows}
}

// Compile validates the schedule and turns it into a Window. It is the single
// boundary where persisted or client-supplied schedule data becomes trusted: an
// unknown zone, a malformed interval, an overlap, or a week with nothing open is
// rejected here rather than yielding a Window the scheduler would misread.
func (s Schedule) Compile() (Window, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		// Most likely cause in production is a binary built without tzdata; the
		// zone name is included so the failure is diagnosable from the message.
		return Window{}, fmt.Errorf("%w: %q", ErrUnknownTimezone, s.Timezone)
	}
	var days [7][]Interval
	for _, w := range s.Windows {
		if w.Weekday < 0 || w.Weekday > 6 {
			return Window{}, fmt.Errorf("%w: weekday %d out of range", ErrBadSchedule, w.Weekday)
		}
		days[w.Weekday] = append(days[w.Weekday], Interval{StartMin: w.StartMinute, EndMin: w.EndMinute})
	}
	win, err := NewWindow(loc, days)
	if err != nil {
		if errors.Is(err, ErrNoOpenInterval) {
			return Window{}, ErrEmptySchedule
		}
		return Window{}, fmt.Errorf("%w: %w", ErrBadSchedule, err)
	}
	return win, nil
}
