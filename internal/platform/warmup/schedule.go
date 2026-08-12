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
	StateUnknown   = "unknown"
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

const (
	bounceWatchRate       = 0.03
	bounceThrottleRate    = 0.05
	bouncePauseRate       = 0.10
	complaintWatchRate    = 0.0003
	complaintThrottleRate = 0.001
	complaintPauseRate    = 0.003

	MinPlacementSamples = 20
	MinBounceSamples    = 50
	MinComplaintSamples = 100
	HealthPolicyVersion = "warmup-phase0-v1"
)

// HealthSignals are normalized counts from immutable observations. A rate is
// actionable only after its corresponding minimum sample count is reached.
type HealthSignals struct {
	Current          string
	Inbox            int
	Spam             int
	BounceSamples    int
	Bounces          int
	ComplaintSamples int
	Complaints       int
	InvalidTokens    int
}

// HealthDecision records both the decision and the exact evidence behind it.
type HealthDecision struct {
	State            string
	ReasonCode       string
	Reason           string
	PlacementSamples int
	SpamRate         float64
	BounceSamples    int
	BounceRate       float64
	ComplaintSamples int
	ComplaintRate    float64
	InvalidTokens    int
}

// wakingStartHour / wakingEndHour bound the recipient-local window in which
// warmup traffic and engagement are allowed. 07:00 inclusive, 22:00 exclusive.
const (
	wakingStartHour = 7
	wakingEndHour   = 22
)

// Recipient-latency tuning. PASSIVE engagement (open / mark-read / rescue-from-
// spam) and WRITING A REPLY are different human acts on very different
// timescales, so they get separately tuned distributions rather than one shared
// dwell. A person glances at a new message within a couple of minutes; nobody
// composes and sends a reply thirty seconds after it arrives, and a reply that
// consistently does is the loudest robotic signal warmup traffic can emit.
const (
	passiveDwellMeanSec = 90.0   // most opens within ~2 min
	passiveDwellMinSec  = 5.0    // floor: a glance is still not instantaneous
	passiveDwellMaxSec  = 3600.0 // cap: an hour is a plausible slow read

	replyDelayMeanSec = 45 * 60.0  // typical reply lands in tens of minutes
	replyDelayMinSec  = 3 * 60.0   // floor: even the fastest human reads, thinks, types
	replyDelayMaxSec  = 8 * 3600.0 // cap: bounded so a draw can't drift a whole day
)

// Coarse day-shape tuning, layered on top of DailyVolumeFactor's ±20% jitter.
// Real business mailboxes go quiet at the weekend and occasionally send nothing
// at all (leave, travel, a head-down day) — a mailbox that sends every single
// calendar day at a steady rate reads as automation on its own.
const (
	weekendFactorLo = 0.15 // weekend traffic drops to a fraction of a weekday's
	weekendFactorHi = 0.40
	skipDayRate     = 0.04 // ~1 zero-send weekday in 25

	// skipDayFloorTarget is the anti-stall guard's threshold: at or below this
	// ramp target a mailbox always sends at least one email, so weekend/skip-day
	// variation can never zero out an early-ramp mailbox and leave warmup making
	// no progress. Above it a genuine zero-send day is allowed.
	skipDayFloorTarget = 8
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

// DailyVolumeFactor scales a day's target so the send curve is neither flat nor
// identical across mailboxes. It is deterministic in (mailboxID, calendar day)
// and layers two scales of variation:
//
//   - fine: a ±20% jitter in [0.8, 1.1], with one deterministic weekday per
//     mailbox as a "lighter" day nudging the factor down;
//   - coarse: a weekend drops to a fraction of a weekday (roughly [0.12, 0.44]
//     after the fine jitter), and ~4% of WEEKDAYS are skipped outright, returning
//     0 — the mailbox sends nothing that day, like a real person on leave.
//
// The overall range is therefore [0, 1.1], NOT the old [0.8, 1.1]: callers must
// handle a zero. Prefer EffectiveDailyVolume, which applies this factor to a ramp
// target together with the anti-stall floor, over calling this directly.
func DailyVolumeFactor(mailboxID string, day time.Time) float64 {
	y, m, d := day.Date()
	key := mailboxID + "|" + strconv.Itoa(y) + "-" + strconv.Itoa(int(m)) + "-" + strconv.Itoa(d)
	factor := scale(hashU64("volume", key), 0.8, 1.1)

	lightDay := int(hashU64("lightday", mailboxID) % 7)
	if int(day.Weekday()) == lightDay {
		factor *= 0.9
	}
	factor = clamp(factor, 0.8, 1.1)

	switch wd := day.Weekday(); {
	case wd == time.Saturday || wd == time.Sunday:
		return factor * scale(hashU64("weekend", key), weekendFactorLo, weekendFactorHi)
	case scale(hashU64("skipday", key), 0, 1) < skipDayRate:
		// A skipped WEEKDAY only: a weekend is already heavily reduced, and
		// stacking a skip on it would cluster quiet days into long silent runs.
		return 0
	default:
		return factor
	}
}

// EffectiveDailyVolume is how many warmup emails a mailbox should actually send
// on `day`: its ramp target shaped by DailyVolumeFactor. It is the ONE home for
// that composition — the due-scheduler and the send path both read it, so they can
// never disagree about a day's quota.
//
// The floor is the anti-stall guard. A mailbox still early in its ramp (target at
// or below skipDayFloorTarget) always sends at least one email, so weekend and
// skip-day variation can never zero out a low-volume mailbox and leave warmup
// making no progress at all. Above that target a true zero-send day is allowed —
// that is the point of the coarse variation. Note the ramp itself is anchored on
// calendar time (see RampTarget's daysWarming), so a quiet day never rewinds the
// ramp; it only lowers that one day's quota.
func EffectiveDailyVolume(target int, mailboxID string, day time.Time) int {
	if target <= 0 {
		return 0
	}
	// DailyVolumeFactor is non-negative and target > 0, so v is never negative.
	v := int(math.Round(float64(target) * DailyVolumeFactor(mailboxID, day)))
	if v < 1 && target <= skipDayFloorTarget {
		return 1
	}
	return v
}

const PairCooldown = 24 * time.Hour

// PairDailyCap prevents a sender from concentrating its daily volume on one
// recipient while still allowing the full target across the eligible pool.
func PairDailyCap(target, eligiblePartners int) int {
	if target <= 0 || eligiblePartners <= 0 {
		return 0
	}
	perPartner := (target + eligiblePartners - 1) / eligiblePartners
	if perPartner < 1 {
		return 1
	}
	return perPartner
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

// EngageDwell is how long a simulated recipient waits before taking the PASSIVE
// actions on a received warmup message — opening it, clearing the unread flag,
// pulling it out of spam. It is heavy-tailed (most dwells short, a long tail of
// slow reads), deterministic in receiptID, and clamped to [5s, 1h].
//
// It deliberately does NOT gate replies: see ReplyDelay / ReplyEngageAfter, which
// are tuned an order of magnitude longer. Widening this clamp instead would be
// wrong — passive engagement genuinely is fast.
func EngageDwell(receiptID string) time.Duration {
	return expDelay("dwell", receiptID, passiveDwellMeanSec, passiveDwellMinSec, passiveDwellMaxSec)
}

// ReplyDelay is how long a simulated recipient waits before REPLYING to a received
// warmup message, measured from the receipt. Same heavy-tailed shape and same
// determinism as EngageDwell but tuned to human reply latency instead of read
// latency: mean 45 minutes, floored at 3 minutes and capped at 8 hours. So the
// bulk of replies land tens of minutes out with a tail into several hours, and the
// floor puts EVERY reply above the range passive dwells typically occupy.
//
// This is the raw draw. Callers want ReplyEngageAfter, which additionally keeps the
// resulting instant inside waking hours.
func ReplyDelay(receiptID string) time.Duration {
	return expDelay("replydelay", receiptID, replyDelayMeanSec, replyDelayMinSec, replyDelayMaxSec)
}

// ReplyEngageAfter is the delay from `now` until a recipient who WILL reply should
// act on a message received at receivedAt: ReplyDelay measured from the receipt,
// snapped into the recipient-local waking window so a long draw can never land a
// reply at 03:00, then re-expressed relative to now.
//
// A target already in the past — the self-healing re-plan of an unengaged duplicate
// receipt, hours after the fact — collapses to "act at the next waking instant"
// rather than waiting all over again, so the result is never negative and never
// itself outside waking hours. A nil location defaults to UTC, matching
// DeferToWakingHours. Pure: `now` is injected, nothing here reads the clock.
func ReplyEngageAfter(receiptID string, receivedAt, now time.Time, loc *time.Location) time.Duration {
	target := DeferToWakingHours(receivedAt.Add(ReplyDelay(receiptID)), loc)
	if !target.After(now) {
		// DeferToWakingHours never moves an instant backwards, so this is >= now.
		target = DeferToWakingHours(now, loc)
	}
	return target.Sub(now)
}

// expDelay draws a heavy-tailed delay: an exponential with the given mean, clamped
// to [minSec, maxSec]. domain namespaces the draw so two delays keyed on the SAME
// id (a receipt's passive dwell and its reply latency) are independent of each
// other rather than perfectly correlated.
func expDelay(domain, key string, meanSec, minSec, maxSec float64) time.Duration {
	// u in (0,1); -mean*ln(u) is exponentially distributed with the given mean.
	u := (float64(hashU64(domain, key)%1_000_000) + 1) / 1_000_001.0
	sec := clamp(-meanSec*math.Log(u), minSec, maxSec)
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

// EvaluateHealth applies minimum sample gates before any rate can influence a
// mailbox. Escalation is immediate; recovery requires enough fresh placement
// evidence and moves one state at a time.
func EvaluateHealth(s HealthSignals) HealthDecision {
	placementSamples := s.Inbox + s.Spam
	d := HealthDecision{
		State: StateHealthy, PlacementSamples: placementSamples,
		SpamRate:      safeRate(s.Spam, placementSamples),
		BounceSamples: s.BounceSamples, BounceRate: safeRate(s.Bounces, s.BounceSamples),
		ComplaintSamples: s.ComplaintSamples, ComplaintRate: safeRate(s.Complaints, s.ComplaintSamples),
		InvalidTokens: s.InvalidTokens,
	}
	setWorst := func(state, code, reason string) {
		if stateRank(state) > stateRank(d.State) {
			d.State, d.ReasonCode, d.Reason = state, code, reason
		}
	}
	if placementSamples >= MinPlacementSamples {
		switch {
		case d.SpamRate > spamPauseRate:
			setWorst(StatePaused, "spam_pause", "spam placement rate above 50%")
		case d.SpamRate > spamThrottleRate:
			setWorst(StateThrottled, "spam_throttle", "spam placement rate above 30%")
		case d.SpamRate > spamWatchRate:
			setWorst(StateWatch, "spam_watch", "spam placement rate above 15%")
		}
	}
	if s.BounceSamples >= MinBounceSamples {
		switch {
		case d.BounceRate > bouncePauseRate:
			setWorst(StatePaused, "bounce_pause", "hard-bounce rate above 10%")
		case d.BounceRate > bounceThrottleRate:
			setWorst(StateThrottled, "bounce_throttle", "hard-bounce rate above 5%")
		case d.BounceRate > bounceWatchRate:
			setWorst(StateWatch, "bounce_watch", "hard-bounce rate above 3%")
		}
	}
	if s.ComplaintSamples >= MinComplaintSamples {
		switch {
		case d.ComplaintRate > complaintPauseRate:
			setWorst(StatePaused, "complaint_pause", "complaint rate above 0.3%")
		case d.ComplaintRate > complaintThrottleRate:
			setWorst(StateThrottled, "complaint_throttle", "complaint rate above 0.1%")
		case d.ComplaintRate > complaintWatchRate:
			setWorst(StateWatch, "complaint_watch", "complaint rate above 0.03%")
		}
	}
	if s.InvalidTokens >= invalidTokenLimit {
		setWorst(StateThrottled, "invalid_tokens", "repeated trusted invalid warmup tokens")
	}

	current := normalizeState(s.Current)
	if d.State == StateHealthy && placementSamples < MinPlacementSamples {
		if stateRank(current) > stateRank(StateHealthy) {
			d.State, d.ReasonCode, d.Reason = current, "insufficient_evidence_to_recover", "not enough recent placement evidence to recover"
		} else {
			d.State, d.ReasonCode, d.Reason = StateUnknown, "placement_sample_insufficient", "not enough recent placement evidence"
		}
		return d
	}
	if stateRank(d.State) < stateRank(current) {
		d.State = stateAtRank(stateRank(current) - 1)
		d.ReasonCode = "recovery_step"
		d.Reason = "clean qualified window: recovering one state"
	}
	// unknown is an ABSENCE of evidence, not a degree of badness, so it shares
	// healthy's rank and the recovery-step branch above never fires for
	// unknown → healthy. That transition still has to name itself: the
	// transitions table requires a non-empty reason_code, and a decision this
	// evaluator cannot explain must never reach the database. Without this the
	// promotion aborts on the CHECK, rolling back the participant update with
	// it, and every mailbox the deploy migration demoted stays unknown forever.
	if d.ReasonCode == "" && d.State != current {
		d.ReasonCode = "evidence_qualified"
		d.Reason = "qualified placement evidence establishes health"
	}
	return d
}

func safeRate(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	if numerator >= denominator {
		return 1
	}
	if numerator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
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
		if want == StateHealthy {
			return lower, "clean window: recovering, stepping down from " + normalizeState(current)
		}
		// Signals improved but aren't fully clean: still step down exactly one
		// level, but report the persisting signal rather than falsely claiming a
		// clean window.
		return lower, "improving, stepping down from " + normalizeState(current) + " to " + lower + " (" + wantReason + ")"
	default:
		if want == StateHealthy {
			return StateHealthy, ""
		}
		return want, wantReason
	}
}

// IsRecovery reports whether a health transition from->to is a recovery (a step
// toward a healthier state) rather than an escalation to a worse one. from == to is
// not a recovery.
func IsRecovery(from, to string) bool {
	return stateRank(to) < stateRank(from)
}

// ShouldApplyTransition reports whether a computed health transition should be
// persisted now. A no-op (from == to) is never applied. An ESCALATION to a worse
// state always applies immediately. A RECOVERY (step down to a healthier state) is
// held back while the timed block is still in force (pausedUntil in the future),
// enforcing the 24h/72h dwell so a mailbox can't walk paused→throttled→watch→healthy
// on back-to-back 5-minute sweeps. A zero/elapsed pausedUntil never blocks recovery.
func ShouldApplyTransition(from, to string, pausedUntil, now time.Time) bool {
	if from == to {
		return false
	}
	if IsRecovery(from, to) && pausedUntil.After(now) {
		return false
	}
	return true
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
	if state == StateUnknown {
		return StateUnknown
	}
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
