package inbox

import (
	"testing"
	"time"
)

// now is the fixed clock every case below is judged against: a Wednesday, so
// year inference and the +30d cap land on unambiguous dates.
var now = time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)

func TestParseReturnDate(t *testing.T) {
	cases := []struct {
		name string
		body string
		// at overrides the clock for cases that need a different vantage point
		// (year inference); zero means `now`.
		at   time.Time
		want time.Time // zero means "must not parse"
	}{
		{name: "iso after a cue", body: "I am away. Back on 2026-08-20.", want: date(2026, time.August, 20)},
		{name: "month first", body: "I'm out of the office, returning on August 20.", want: date(2026, time.August, 20)},
		{name: "month first abbreviated with ordinal", body: "Away — back on Aug 20th, 2026.", want: date(2026, time.August, 20)},
		{name: "day first", body: "On leave, back on 20 August 2026.", want: date(2026, time.August, 20)},
		{name: "day first with of", body: "I will return on the 20th of August.", want: date(2026, time.August, 20)},
		{name: "until names the last absent day", body: "I am away until 20 August.", want: date(2026, time.August, 20)},
		{
			name: "year rolls forward when omitted",
			body: "Back on January 6.",
			at:   date(2026, time.December, 28),
			want: date(2027, time.January, 6),
		},

		// Everything below must NOT parse: the caller then falls through to
		// today's tag-only behaviour, which is always safer than a guess.
		{name: "no cue phrase at all", body: "Thanks for your email. Sent 20 August 2026 from my phone."},
		{name: "cue but no date", body: "I am away for a while, back soon."},
		{name: "ambiguous numeric date", body: "I am back on 08/09/2026."},
		{name: "date already past", body: "I was away until 20 July 2026."},
		{name: "today is not a deferral", body: "Back on 2026-08-05."},
		{name: "impossible calendar date", body: "Back on 2026-02-31."},
		{name: "unknown month word", body: "Back on Smarch 20."},
		{name: "date too far from the cue", body: "Back on the day my project finishes, which should be around 20 August 2026."},
		{name: "empty body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := tc.at
			if at.IsZero() {
				at = now
			}
			got, ok := parseReturnDate(tc.body, at)
			if tc.want.IsZero() {
				if ok {
					t.Fatalf("expected no deferral, got %v", got)
				}
				return
			}
			if !ok {
				t.Fatalf("expected %v, got no deferral", tc.want)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("parseReturnDate = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestParseReturnDateCapsAtThirtyDays: a stated return far in the future is
// honoured only up to the cap, so a mistyped year cannot park an enrollment for
// a century.
func TestParseReturnDateCapsAtThirtyDays(t *testing.T) {
	got, ok := parseReturnDate("Back on 2126-08-20.", now)
	if !ok {
		t.Fatal("a far-future date should still defer, capped")
	}
	if want := now.Add(maxDeferral); !got.Equal(want) {
		t.Fatalf("parseReturnDate = %v, want the cap %v", got, want)
	}
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
