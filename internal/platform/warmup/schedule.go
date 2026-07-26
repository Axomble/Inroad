package warmup

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strconv"
	"time"
)

// Health states, ordered from best to worst. These exact strings are the shared
// contract pinned by migration 000018's CHECK constraint on
// warmup_participants.health_state.
const (
	StateHealthy   = "healthy"
	StateWatch     = "watch"
	StateThrottled = "throttled"
	StatePaused    = "paused"
)

// Health thresholds (spec §8), evaluated over a trailing window by the caller.
const (
	spamWatchRate     = 0.15 // spam placement above this → watch
	spamThrottleRate  = 0.30 // → throttled
	spamPauseRate     = 0.50 // → paused
	bounceSpikeRate   = 0.10 // hard-bounce rate above this → paused
	invalidTokenLimit = 3    // sustained invalid/tampered tokens → throttled
)

// wakingStartHour / wakingEndHour bound the recipient-local window in which
// warmup traffic and engagement are allowed. 07:00 inclusive, 22:00 exclusive.
const (
	wakingStartHour = 7
	wakingEndHour   = 22
)

// RampTarget is the day's target send volume: start + daysWarming*increment,
// capped at maxVol and never below startVol (a negative day count clamps to 0).
func RampTarget(startVol, maxVol, increment, daysWarming int) int {
	if daysWarming < 0 {
		daysWarming = 0
	}
	target := startVol + daysWarming*increment
	if target > maxVol {
		return maxVol
	}
	if target < startVol {
		return startVol
	}
	return target
}

// DailyVolumeFactor scales a day's target to avoid a perfectly flat send curve.
// It is deterministic in (mailboxID, calendar day) and always in [0.8, 1.1]. One
// deterministic weekday per mailbox is a "lighter" day, nudging the factor down.
func DailyVolumeFactor(mailboxID string, day time.Time) float64 {
	y, m, d := day.Date()
	key := mailboxID + "|" + strconv.Itoa(y) + "-" + strconv.Itoa(int(m)) + "-" + strconv.Itoa(d)
	factor := scale(hashU64("volume", key), 0.8, 1.1)

	lightDay := int(hashU64("lightday", mailboxID) % 7)
	if int(day.Weekday()) == lightDay {
		factor *= 0.9
	}
	return clamp(factor, 0.8, 1.1)
}

// NextSpacing is the delay before the index-th send of a day whose target volume
// is `target`. It spreads `target` sends across the waking window and applies a
// deterministic multiplicative jitter in [0.6, 1.4) so sends never land on fixed
// clock boundaries. Returns 0 for a non-positive target (nothing to space).
func NextSpacing(target int, mailboxID string, index int) time.Duration {
	if target <= 0 {
		return 0
	}
	window := time.Duration(wakingEndHour-wakingStartHour) * time.Hour
	base := window / time.Duration(target)
	jitter := scale(hashU64("spacing", mailboxID, strconv.Itoa(index)), 0.6, 1.4)
	return time.Duration(float64(base) * jitter)
}

// EngageDwell is how long a simulated recipient waits before acting on a received
// warmup message. It is heavy-tailed (most dwells short, a long tail of slow
// reads) via an inverse-exponential transform, deterministic in receiptID, and
// clamped to [5s, 1h] so it stays plausible and bounded.
func EngageDwell(receiptID string) time.Duration {
	const meanSec = 90.0
	// u in (0,1); -mean*ln(u) is exponentially distributed with the given mean.
	u := (float64(hashU64("dwell", receiptID)%1_000_000) + 1) / 1_000_001.0
	sec := clamp(-meanSec*math.Log(u), 5, 3600)
	return time.Duration(sec * float64(time.Second))
}

// DeferToWakingHours returns t unchanged if its local hour is within
// [07:00, 22:00); otherwise it moves the instant to 07:00 local — the same
// morning when t is after midnight but before 07:00, the next morning when t is
// at or after 22:00. A nil location defaults to UTC.
func DeferToWakingHours(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := t.In(loc)
	hour := local.Hour()
	if hour >= wakingStartHour && hour < wakingEndHour {
		return t
	}
	morning := time.Date(local.Year(), local.Month(), local.Day(), wakingStartHour, 0, 0, 0, loc)
	if hour >= wakingEndHour {
		morning = morning.AddDate(0, 0, 1)
	}
	return morning
}

// HealthState computes the next health state from the trailing-window signals and
// the current state. Escalation is immediate to the worst warranted level;
// recovery is gradual — a clean window steps the state down exactly one level, so
// a paused mailbox climbs paused→throttled→watch→healthy over successive clean
// evaluations rather than snapping back. It returns the new state and a
// human-readable reason.
func HealthState(spamRate, bounceRate float64, invalidTokens int, current string) (state, reason string) {
	want, wantReason := worstSignalState(spamRate, bounceRate, invalidTokens)
	curRank := stateRank(current)
	wantRank := stateRank(want)

	switch {
	case wantRank > curRank:
		return want, wantReason
	case wantRank < curRank:
		lower := stateAtRank(curRank - 1)
		if lower == StateHealthy {
			return StateHealthy, ""
		}
		return lower, "clean window: recovering, stepping down from " + normalizeState(current)
	default:
		if want == StateHealthy {
			return StateHealthy, ""
		}
		return want, wantReason
	}
}

// worstSignalState maps the raw signals to the worst state they warrant, checked
// most-severe first.
func worstSignalState(spamRate, bounceRate float64, invalidTokens int) (state, reason string) {
	switch {
	case spamRate > spamPauseRate:
		return StatePaused, "spam placement rate above 50%"
	case bounceRate > bounceSpikeRate:
		return StatePaused, "hard-bounce spike"
	case spamRate > spamThrottleRate:
		return StateThrottled, "spam placement rate above 30%"
	case invalidTokens >= invalidTokenLimit:
		return StateThrottled, "repeated invalid or tampered warmup tokens"
	case spamRate > spamWatchRate:
		return StateWatch, "spam placement rate above 15%"
	default:
		return StateHealthy, ""
	}
}

// stateRank orders the states; an unknown/empty current state is treated as
// healthy so recovery math never underflows.
func stateRank(state string) int {
	switch state {
	case StateWatch:
		return 1
	case StateThrottled:
		return 2
	case StatePaused:
		return 3
	default:
		return 0
	}
}

func stateAtRank(rank int) string {
	switch rank {
	case 1:
		return StateWatch
	case 2:
		return StateThrottled
	case 3:
		return StatePaused
	default:
		return StateHealthy
	}
}

func normalizeState(state string) string {
	return stateAtRank(stateRank(state))
}

// hashU64 is the package's shared deterministic hash: a SHA-256 over the
// NUL-joined parts, folded to a uint64. NUL-joining keeps ("a","bc") distinct
// from ("ab","c"). It replaces math/rand so every "jitter" here is reproducible
// and table-testable.
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

// scale maps a hash to a float in [lo, hi) using 1000 buckets of resolution.
func scale(h uint64, lo, hi float64) float64 {
	return lo + float64(h%1000)/1000.0*(hi-lo)
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
