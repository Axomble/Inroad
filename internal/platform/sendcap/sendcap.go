// Package sendcap is the arithmetic of "how much may this mailbox send today":
// the ramp schedule, and the warmup-health scaling applied on top of it for COLD
// sending. Pure — no database, no clock, no config — so both the execution plane
// (which enforces the cap) and the API (which reports it to an operator) compute
// the identical number from the identical inputs. A second implementation of this
// math is a bug: the panel would promise a capacity the sender does not honour.
package sendcap

// Warmup health states, mirroring the warmup_participants.health_state CHECK
// constraint. Duplicated as plain strings rather than imported: platform packages
// do not depend on each other, and this one must stay free of anything but its
// own arithmetic.
const (
	HealthWatch     = "watch"
	HealthThrottled = "throttled"
	HealthPaused    = "paused"
)

// Effective returns today's allowed daily send count for a mailbox given its ramp
// schedule and age in days. Linear from startCap to dailyCap over rampDays.
func Effective(dailyCap, startCap, rampDays int, rampEnabled bool, ageDays int) int {
	if !rampEnabled || ageDays >= rampDays || rampDays <= 0 {
		return dailyCap
	}
	if ageDays <= 0 {
		return startCap
	}
	return startCap + (dailyCap-startCap)*ageDays/rampDays
}

// ColdFactor scales a mailbox's effective daily cap by its warmup health, for
// COLD sending only (warmup's own scheduling is untouched). The warmup engine
// moves a mailbox to 'watch'/'throttled' on a worsening trailing spam-placement
// rate and to 'paused' when it is in real trouble; before this, cold volume
// ignored all of it.
//
// paused returns 0, and callers MUST treat 0 as "cannot send right now" rather
// than as a daily cap of zero: a cap of zero stops an enrollment permanently,
// whereas an unhealthy mailbox may recover, so its threads have to wait for it.
//
// An empty state is a mailbox that is not warming up at all, which is not a
// health signal and is therefore ungated. So is any unrecognized value: a stored
// state outside the CHECK constraint can only come from a direct write, and
// silently halving a healthy mailbox on a typo would be worse than ignoring it.
func ColdFactor(healthState string) float64 {
	switch healthState {
	case HealthWatch:
		return 0.7
	case HealthThrottled:
		return 0.5
	case HealthPaused:
		return 0
	default:
		return 1
	}
}

// Cold is a mailbox's cold-sending cap for today: its ramped effective cap scaled
// by warmup health. Zero means "cannot send" (paused, or a cap of zero to begin
// with) — see ColdFactor.
//
// Scaling rounds DOWN, because the point is to send less. It floors at 1 for a
// gated-but-not-paused mailbox whose cap is too small to halve (cap 1, throttled)
// so that throttling never silently becomes pausing: 'watch' and 'throttled' mean
// "slower", and only 'paused' means "stop".
func Cold(effective int, healthState string) int {
	factor := ColdFactor(healthState)
	if factor <= 0 || effective <= 0 {
		return 0
	}
	scaled := int(float64(effective) * factor)
	if scaled < 1 {
		return 1
	}
	return scaled
}
