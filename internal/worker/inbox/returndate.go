package inbox

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// maxDeferral caps how far an out-of-office reply may push a sequence out. A
// stated return date beyond it is honoured only up to the cap: a mis-parsed or
// mistyped year ("back on 2 January 2126") must not park an enrollment for a
// century, and an operator can always stop it by hand within a month.
const maxDeferral = 30 * 24 * time.Hour

// returnCues are the phrases that introduce a stated return date. Requiring one
// is what keeps this narrow: an out-of-office body is full of dates (the date
// it was sent, a meeting, a signature), and only a date the sender ATTACHED to
// their return may move a sequence. No cue ⇒ no deferral.
//
// "until"/"till"/"through" name the last day away rather than the first day
// back; the one-day difference is deliberately ignored, because deferring to
// the morning of the last absent day is already the conservative direction
// versus sending during it, and pretending to that precision on free text
// invites more error than it removes.
var returnCues = regexp.MustCompile(`(?i)\b(?:back|returning|return|available|reachable|in the office|at my desk)\b[^.\n]{0,30}?\b(?:on|from)\b|\b(?:until|till|thru|through)\b`)

// isoDate matches an unambiguous YYYY-MM-DD.
var isoDate = regexp.MustCompile(`\b(\d{4})-(\d{1,2})-(\d{1,2})\b`)

// monthFirst matches "August 15", "Aug 15th", "Aug. 15, 2026".
var monthFirst = regexp.MustCompile(`(?i)\b([a-z]{3,9})\.?\s+(\d{1,2})(?:st|nd|rd|th)?(?:\s*,?\s*(\d{4}))?\b`)

// dayFirst matches "15 August", "15th of Aug 2026".
var dayFirst = regexp.MustCompile(`(?i)\b(\d{1,2})(?:st|nd|rd|th)?\s+(?:of\s+)?([a-z]{3,9})\.?(?:\s*,?\s*(\d{4}))?\b`)

// months maps the English month names and their common three-letter
// abbreviations (plus "sept") onto time.Month. Purely-numeric forms are
// DELIBERATELY unsupported: "8/9" is 8 September to half the world and 9 August
// to the other half, and guessing wrong parks a live sequence for a month.
// Failing to parse costs nothing — the caller falls through to tag-only.
var months = map[string]time.Month{
	"jan": time.January, "january": time.January,
	"feb": time.February, "february": time.February,
	"mar": time.March, "march": time.March,
	"apr": time.April, "april": time.April,
	"may": time.May,
	"jun": time.June, "june": time.June,
	"jul": time.July, "july": time.July,
	"aug": time.August, "august": time.August,
	"sep": time.September, "sept": time.September, "september": time.September,
	"oct": time.October, "october": time.October,
	"nov": time.November, "november": time.November,
	"dec": time.December, "december": time.December,
}

// searchWindow is how much text after a return cue is searched for a date. Wide
// enough for "back in the office on Monday, 15 August 2026", narrow enough that
// the next sentence's date cannot be mistaken for a return date.
const searchWindow = 48

// parseReturnDate finds a stated return date in an out-of-office body and
// returns when the sequence should resume.
//
// It is deliberately conservative: ok=false on anything it is not sure of, and
// the caller must then NOT defer — it falls through to today's tag-only
// behaviour. A wrong parse is far more expensive than no parse, because it
// either sends into the absence anyway or silently parks a live sequence.
//
// The returned time is midnight UTC on the return day, clamped to
// (now, now+maxDeferral]. A date already past is not a deferral (they are back)
// and reports ok=false.
func parseReturnDate(body string, now time.Time) (time.Time, bool) {
	cue := returnCues.FindStringIndex(body)
	if cue == nil {
		return time.Time{}, false
	}
	tail := body[cue[1]:]
	if len(tail) > searchWindow {
		tail = tail[:searchWindow]
	}
	day, ok := findDate(tail, now)
	if !ok {
		return time.Time{}, false
	}
	if !day.After(now) {
		return time.Time{}, false
	}
	if latest := now.Add(maxDeferral); day.After(latest) {
		return latest, true
	}
	return day, true
}

// findDate returns midnight UTC of the first date in text, trying the
// unambiguous ISO form first and then the two English orderings.
func findDate(text string, now time.Time) (time.Time, bool) {
	if m := isoDate.FindStringSubmatch(text); m != nil {
		return assemble(atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3]), now)
	}
	if m := monthFirst.FindStringSubmatch(text); m != nil {
		if month, known := months[strings.ToLower(m[1])]; known {
			return assemble(atoi(m[3]), month, atoi(m[2]), now)
		}
	}
	if m := dayFirst.FindStringSubmatch(text); m != nil {
		if month, known := months[strings.ToLower(m[2])]; known {
			return assemble(atoi(m[3]), month, atoi(m[1]), now)
		}
	}
	return time.Time{}, false
}

// assemble builds midnight UTC for the given date, inferring the year when the
// sender omitted it (an omitted year means the next occurrence, so a "back on
// 3 January" sent in December rolls into next year) and rejecting anything that
// does not round-trip — time.Date normalises 31 February into 3 March, which is
// a parse failure, not a date.
func assemble(year int, month time.Month, day int, now time.Time) (time.Time, bool) {
	if day < 1 || day > 31 || month < time.January || month > time.December {
		return time.Time{}, false
	}
	if year == 0 {
		year = now.UTC().Year()
		candidate := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
		if candidate.Before(now.UTC().Truncate(24 * time.Hour)) {
			year++
		}
	}
	at := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	if at.Year() != year || at.Month() != month || at.Day() != day {
		return time.Time{}, false
	}
	return at, true
}

// atoi returns 0 for an absent optional capture group, which assemble reads as
// "year omitted". Every group reaching it is already \d+-constrained by the
// pattern, so a genuine parse error is impossible.
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
