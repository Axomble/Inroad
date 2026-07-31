// Package cadence decides the instant a cold send actually goes out: it snaps a
// desired time forward into a campaign's weekly sending window and pushes it off
// the clock grid.
//
// Nothing here touches Postgres, the network, the clock, or math/rand. Every
// "jitter" is a seeded hash of stable identifiers, so a retried task recomputes
// the same instant — the enrollment's next_due_at cursor and the enqueued task
// time stay equal by construction, which is what keeps the sweeper from
// double-firing a send. Same rule as platform/warmup, for the same reason.
//
// Cold sending only. Warmup keeps its own waking-hours scheduler; the two
// engines are deliberately independent.
package cadence

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// MinutesPerDay bounds an interval: minutes elapsed from local midnight, so a
// day's last representable end is 1440 (exclusive midnight).
const MinutesPerDay = 24 * 60

// maxDayWalk bounds the forward search for an open interval. A validated Window
// has at least one open interval in the week, so 8 days is always enough; the
// bound exists so a corrupted row returns an error instead of looping.
const maxDayWalk = 8

// ErrNoOpenInterval means the window has no open interval anywhere in the week,
// so no send instant exists. Validate() rejects this, and every campaign is
// created with a default window — a Window in this state indicates corrupted
// persisted state, and callers must surface it rather than sending immediately.
var ErrNoOpenInterval = errors.New("cadence: window has no open interval")

// Interval is a half-open [StartMin, EndMin) span of minutes from local
// midnight. Half-open so that 09:00–12:00 and 12:00–17:00 are adjacent, not
// overlapping.
type Interval struct {
	StartMin int
	EndMin   int
}

// Window is a campaign's weekly sending schedule, evaluated in a fixed IANA
// location. Days is indexed by time.Weekday (0 = Sunday); each day's intervals
// are sorted and non-overlapping — guaranteed on the way in by Validate and, in
// the database, by a GiST exclusion constraint.
type Window struct {
	Loc  *time.Location
	Days [7][]Interval
}

// NewWindow builds a Window from per-weekday intervals, sorting each day and
// validating the result. Intervals are copied, so the caller's slices are not
// aliased or mutated. A nil location defaults to UTC.
func NewWindow(loc *time.Location, days [7][]Interval) (Window, error) {
	if loc == nil {
		loc = time.UTC
	}
	w := Window{Loc: loc}
	for d, intervals := range days {
		if len(intervals) == 0 {
			continue
		}
		day := make([]Interval, len(intervals))
		copy(day, intervals)
		sort.Slice(day, func(i, j int) bool { return day[i].StartMin < day[j].StartMin })
		w.Days[d] = day
	}
	if err := w.Validate(); err != nil {
		return Window{}, err
	}
	return w, nil
}

// Validate reports the first structural problem with the window: an out-of-range
// or inverted interval, an overlap within a day, or no open interval at all.
// Days must already be sorted (NewWindow does this).
func (w Window) Validate() error {
	open := false
	for d, intervals := range w.Days {
		prevEnd := -1
		for _, iv := range intervals {
			switch {
			case iv.StartMin < 0 || iv.StartMin >= MinutesPerDay:
				return fmt.Errorf("cadence: weekday %d: start minute %d out of range", d, iv.StartMin)
			case iv.EndMin <= 0 || iv.EndMin > MinutesPerDay:
				return fmt.Errorf("cadence: weekday %d: end minute %d out of range", d, iv.EndMin)
			case iv.StartMin >= iv.EndMin:
				return fmt.Errorf("cadence: weekday %d: interval %d-%d is empty or inverted", d, iv.StartMin, iv.EndMin)
			case iv.StartMin < prevEnd:
				return fmt.Errorf("cadence: weekday %d: interval %d-%d overlaps the previous one", d, iv.StartMin, iv.EndMin)
			}
			prevEnd = iv.EndMin
			open = true
		}
	}
	if !open {
		return ErrNoOpenInterval
	}
	return nil
}

// OpenDuration is the total open sending time on the local calendar day
// containing t. Measured in wall-clock minutes, so it does not account for a DST
// transition inside the day — callers use it to spread a batch, where a
// one-hour error over a day is immaterial.
func (w Window) OpenDuration(t time.Time) time.Duration {
	var mins int
	for _, iv := range w.Days[w.localOf(t).Weekday()] {
		mins += iv.EndMin - iv.StartMin
	}
	return time.Duration(mins) * time.Minute
}

// Next returns the first instant at or after t that falls inside an open
// interval, humanized off the clock grid. key seeds the humanization: pass a
// stable identifier (an enrollment or send id) so a retry recomputes the same
// instant.
//
// Snapping only ever moves an instant later (never earlier), so an overdue send
// is delayed no further than the next window open.
func (w Window) Next(t time.Time, key string) (time.Time, error) {
	if key == "" {
		return time.Time{}, errors.New("cadence: Next requires a non-empty key")
	}
	loc := w.location()
	local := t.In(loc)

	for day := range maxDayWalk {
		date := local.AddDate(0, 0, day)
		// Only the first day starts mid-day; later days are searched from 00:00.
		floor := 0
		if day == 0 {
			floor = minuteOfDay(local)
		}
		for _, iv := range w.Days[date.Weekday()] {
			if iv.EndMin <= floor {
				continue // interval already closed relative to t
			}
			// max: when floor is inside this interval, keep t's own position
			// rather than snapping backward to the interval start.
			start := max(iv.StartMin, floor)
			return w.humanize(date, start, iv, key), nil
		}
	}
	return time.Time{}, ErrNoOpenInterval
}

// NextAfterOffset resolves a desired offset into the open time of the window,
// counted forward from t across as many days as it takes. It is how a batch of
// sends spread over "6 hours of open window" becomes real instants: an offset
// larger than what remains today rolls into tomorrow's intervals rather than
// spilling past a window edge.
func (w Window) NextAfterOffset(t time.Time, offset time.Duration, key string) (time.Time, error) {
	if offset < 0 {
		offset = 0
	}
	if key == "" {
		return time.Time{}, errors.New("cadence: NextAfterOffset requires a non-empty key")
	}
	loc := w.location()
	local := t.In(loc)
	remaining := int(offset / time.Minute)

	for day := range maxDayWalk {
		date := local.AddDate(0, 0, day)
		floor := 0
		if day == 0 {
			floor = minuteOfDay(local)
		}
		for _, iv := range w.Days[date.Weekday()] {
			if iv.EndMin <= floor {
				continue
			}
			start := max(iv.StartMin, floor)
			available := iv.EndMin - start
			if remaining < available {
				return w.humanize(date, start+remaining, iv, key), nil
			}
			// This interval is fully consumed by the offset; carry the rest on.
			remaining -= available
		}
	}
	return time.Time{}, ErrNoOpenInterval
}

// humanize converts a local date + minute-of-day into a real instant and nudges
// it off the clock grid: a deterministic 1–59 second offset plus a sub-second
// component, so no computed send time has Second() == 0.
//
// The nudge is applied BEFORE the final clamp into iv, never after, so jitter
// can never push an instant past the interval's end. When the target minute is
// the interval's last one, the nudge is subtracted instead of added, keeping the
// instant inside [start, end) while staying off the grid.
func (w Window) humanize(date time.Time, minute int, iv Interval, key string) time.Time {
	h := hashU64("humanize", key, strconv.Itoa(minute))
	sec := int(h%59) + 1                   // 1..59, never 0
	nanos := int((h >> 8) % 1_000_000_000) //nolint:gosec // bounded by the modulus
	base := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, w.location())

	if minute >= iv.EndMin-1 {
		// Last minute of the interval: nudge backward off the boundary so the
		// result stays strictly inside the window (invariant: no send outside an
		// open interval).
		end := base.Add(time.Duration(iv.EndMin) * time.Minute)
		return end.Add(-(time.Duration(sec)*time.Second + time.Duration(nanos)))
	}
	return base.Add(time.Duration(minute)*time.Minute + time.Duration(sec)*time.Second + time.Duration(nanos))
}

// Contains reports whether t falls inside an open interval of the window. Used
// by tests and by callers that want to explain a deferral, not to gate sends —
// scheduling already guarantees placement.
func (w Window) Contains(t time.Time) bool {
	local := w.localOf(t)
	minute := minuteOfDay(local)
	for _, iv := range w.Days[local.Weekday()] {
		if minute >= iv.StartMin && minute < iv.EndMin {
			return true
		}
	}
	return false
}

func (w Window) location() *time.Location {
	if w.Loc == nil {
		return time.UTC
	}
	return w.Loc
}

func (w Window) localOf(t time.Time) time.Time { return t.In(w.location()) }

// minuteOfDay is t's minute offset from local midnight, rounding a partial
// minute down so an instant is never treated as later than it is.
func minuteOfDay(local time.Time) int { return local.Hour()*60 + local.Minute() }

// hashU64 is the package's shared deterministic hash: SHA-256 over the
// NUL-joined parts, folded to a uint64. NUL-joining keeps ("a","bc") distinct
// from ("ab","c"). It replaces math/rand so every jitter is reproducible and
// table-testable.
func hashU64(parts ...string) uint64 {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return binary.BigEndian.Uint64(h.Sum(nil)[:8])
}
