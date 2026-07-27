package warmup

import (
	"math"
	"time"
)

// DueInputs are the participant + counter values NextDue reasons over. It is a
// pure snapshot: the caller (coreapi) loads these from warmup_participants and
// warmup_daily_stats, so this function touches no database, clock, or rand and is
// table-testable in isolation.
type DueInputs struct {
	MailboxID   string
	StartVolume int
	MaxVolume   int
	Increment   int
	StartedAt   time.Time // when the mailbox began warming (ramp anchor)
	SentToday   int       // warmup emails already sent today (UTC)
	HealthState string    // participant.health_state
	PausedUntil time.Time // zero when not paused
	Now         time.Time
	Loc         *time.Location // recipient-local window; nil = UTC
}

// DuePlan is the scheduling verdict for one mailbox: SendNow reports whether a
// warmup email is due right now (under target and inside the waking window);
// NextDue is when the following tick should be scheduled (the lazy chain reads
// this after a send to enqueue the next one, spaced + deferred to waking hours).
type DuePlan struct {
	NextDue time.Time
	SendNow bool
}

// NextDue computes the scheduling verdict from ramp target, per-day volume
// factor, inter-send spacing, and the waking-hours window — pure policy over the
// participant snapshot. A paused/over-target/pre-window mailbox never sends now;
// NextDue always points at the next plausible send instant so the chain keeps
// making progress rather than stalling.
func NextDue(in DueInputs) DuePlan {
	// Health pause dominates: no send until the pause lifts.
	if in.HealthState == StatePaused || in.Now.Before(in.PausedUntil) {
		next := in.PausedUntil
		if !next.After(in.Now) {
			next = in.Now.Add(time.Hour)
		}
		return DuePlan{NextDue: DeferToWakingHours(next, in.Loc), SendNow: false}
	}

	days := int(in.Now.Sub(in.StartedAt).Hours() / 24)
	target := RampTarget(in.StartVolume, in.MaxVolume, in.Increment, days)
	effective := int(math.Round(float64(target) * DailyVolumeFactor(in.MailboxID, in.Now)))
	if effective < 0 {
		effective = 0
	}

	// Today's quota met → next send is tomorrow morning.
	if in.SentToday >= effective {
		return DuePlan{NextDue: DeferToWakingHours(in.Now.AddDate(0, 0, 1), in.Loc), SendNow: false}
	}

	spacing := NextSpacing(effective, in.MailboxID, in.SentToday)
	next := DeferToWakingHours(in.Now.Add(spacing), in.Loc)
	sendNow := withinWaking(in.Now, in.Loc)
	return DuePlan{NextDue: next, SendNow: sendNow}
}

// withinWaking reports whether t's local hour is inside [wakingStartHour,
// wakingEndHour). A nil location defaults to UTC (matches DeferToWakingHours).
func withinWaking(t time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	hour := t.In(loc).Hour()
	return hour >= wakingStartHour && hour < wakingEndHour
}
