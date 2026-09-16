package warmup

import (
	"testing"
	"time"
)

// baseInputs is a healthy mid-window mailbox with a comfortable ramp target; each
// test tweaks the one field it exercises.
func baseInputs(now time.Time) DueInputs {
	return DueInputs{
		MailboxID:   "mbox-1",
		StartVolume: 10,
		MaxVolume:   40,
		Increment:   2,
		StartedAt:   now.AddDate(0, 0, -5), // 5 days warming
		SentToday:   0,
		HealthState: StateHealthy,
		Now:         now,
	}
}

// noon keeps Now inside the [07:00,22:00) waking window so window logic doesn't
// confound the target/pause assertions. It is deliberately a MONDAY and, for
// baseInputs' mailbox, not one of the deterministically skipped weekdays — see
// TestNoonIsAnOrdinarySendingDay. Otherwise DailyVolumeFactor's coarse day shape
// would quietly shrink (or zero) the target these tests assume is comfortable.
func noon() time.Time {
	return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
}

// TestNoonIsAnOrdinarySendingDay guards the fixture itself. Every test below reasons
// about a "comfortable target", which only holds on a full-volume weekday; if a
// retuning of the weekend/skip-day shape ever moves this date into a quiet day, this
// fails with a clear reason instead of the target assertions failing mysteriously.
func TestNoonIsAnOrdinarySendingDay(t *testing.T) {
	now := noon()
	if wd := now.Weekday(); wd == time.Saturday || wd == time.Sunday {
		t.Fatalf("fixture date %s is a %s; these tests assume a full-volume weekday", now.Format("2006-01-02"), wd)
	}
	if f := DailyVolumeFactor(baseInputs(now).MailboxID, now); f < 0.8 {
		t.Fatalf("fixture day factor %f < 0.8 — %s is a quiet/skipped day for this mailbox", f, now.Format("2006-01-02"))
	}
}

func TestNextDueSendsNowWhenUnderTargetInWindow(t *testing.T) {
	plan := NextDue(baseInputs(noon()))
	if !plan.SendNow {
		t.Fatalf("expected SendNow under target inside waking window")
	}
	if plan.NextDue.IsZero() {
		t.Fatalf("expected a non-zero next-due time")
	}
}

func TestNextDueNotNowOutsideWakingWindow(t *testing.T) {
	night := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC) // 03:00, before the window
	in := baseInputs(night)
	plan := NextDue(in)
	if plan.SendNow {
		t.Fatalf("expected no send at 03:00 (outside waking window)")
	}
	// Next due is deferred to the same morning's 07:00.
	if h := plan.NextDue.Hour(); h != wakingStartHour {
		t.Fatalf("expected next-due at %02d:00, got %02d:00", wakingStartHour, h)
	}
}

func TestNextDueQuotaMetDefersToTomorrow(t *testing.T) {
	in := baseInputs(noon())
	in.SentToday = 1000 // far over any plausible target
	plan := NextDue(in)
	if plan.SendNow {
		t.Fatalf("expected no send once today's quota is met")
	}
	if !plan.NextDue.After(in.Now) {
		t.Fatalf("expected next-due in the future, got %v (now %v)", plan.NextDue, in.Now)
	}
	if plan.NextDue.Day() == in.Now.Day() {
		t.Fatalf("expected next-due to roll to a later day, got same day %v", plan.NextDue)
	}
}

func TestNextDuePausedNeverSends(t *testing.T) {
	in := baseInputs(noon())
	in.HealthState = StatePaused
	plan := NextDue(in)
	if plan.SendNow {
		t.Fatalf("a paused participant must never send now")
	}
}

func TestNextDuePausedUntilInFutureNeverSends(t *testing.T) {
	in := baseInputs(noon())
	in.HealthState = StateHealthy
	in.PausedUntil = in.Now.Add(2 * time.Hour)
	plan := NextDue(in)
	if plan.SendNow {
		t.Fatalf("a mailbox paused_until the future must never send now")
	}
	if !plan.NextDue.After(in.Now) {
		t.Fatalf("expected next-due after the pause window, got %v", plan.NextDue)
	}
}

// TestNextDueRampBoundedByStartVolume proves the effective target never lets a
// fresh mailbox exceed its own ramp: a day-0 mailbox with nothing sent still sends,
// while one far over its ramped ceiling stops.
func TestNextDueRampBoundedByStartVolume(t *testing.T) {
	now := noon()
	in := baseInputs(now)
	in.StartedAt = now // day 0
	in.StartVolume = 4 // target ~= 4 * volume factor
	in.MaxVolume = 40
	in.SentToday = 0
	if plan := NextDue(in); !plan.SendNow {
		t.Fatalf("day-0 mailbox with 0 sent should still send")
	}
	// At a count comfortably above the ramped start ceiling it must stop.
	in.SentToday = 100
	if plan := NextDue(in); plan.SendNow {
		t.Fatalf("mailbox far over its ramp ceiling should not send")
	}
}

// The spacing floor. NextSpacing computes the gap that makes a ramp look human
// — waking window ÷ today's target — but before this it was only ever used to
// schedule the NEXT tick, never to refuse an early one. Any other trigger for
// the same mailbox (the 5-minute warmup:sweep fans one out for every enabled
// participant, at `now`) therefore sent immediately, so a restart emptied the
// day's whole quota back-to-back in minutes. Volume stayed correct; the pacing
// warmup exists to produce did not.
func TestSendNowRefusedInsideTheSpacingFloor(t *testing.T) {
	now := noon()
	in := baseInputs(now)
	in.SentToday = 1
	spacing := NextSpacing(EffectiveDailyVolume(RampTarget(in.StartVolume, in.MaxVolume, in.Increment, 5), in.MailboxID, now), in.MailboxID, in.SentToday)
	// The previous send is recent enough that the floor has not elapsed.
	in.LastSentAt = now.Add(-spacing / 2)

	plan := NextDue(in)

	if plan.SendNow {
		t.Fatalf("sent %s ago with a %s floor: SendNow must be false", spacing/2, spacing)
	}
	// And the chain must point at the moment the floor lifts, so the mailbox
	// resumes on its own rather than waiting for the next sweep.
	if want := in.LastSentAt.Add(spacing); !plan.NextDue.Equal(want) {
		t.Fatalf("NextDue = %s, want the floor's end %s", plan.NextDue, want)
	}
}

func TestSendNowAllowedOnceTheSpacingFloorHasElapsed(t *testing.T) {
	now := noon()
	in := baseInputs(now)
	in.SentToday = 1
	spacing := NextSpacing(EffectiveDailyVolume(RampTarget(in.StartVolume, in.MaxVolume, in.Increment, 5), in.MailboxID, now), in.MailboxID, in.SentToday)
	in.LastSentAt = now.Add(-spacing - time.Minute)

	if plan := NextDue(in); !plan.SendNow {
		t.Fatalf("floor elapsed (%s ago, floor %s): SendNow must be true", spacing+time.Minute, spacing)
	}
}

// A mailbox that has never sent must not be held back: the floor is a gap
// BETWEEN sends, and a zero LastSentAt means there is no previous send to
// measure from. Without this, warmup would never start.
func TestFirstEverSendIsNotHeldBackByTheFloor(t *testing.T) {
	in := baseInputs(noon())
	in.SentToday = 0
	in.LastSentAt = time.Time{}

	if plan := NextDue(in); !plan.SendNow {
		t.Fatal("a mailbox that has never sent must be allowed its first warmup send")
	}
}

// The floor must not resurrect a mailbox that is out of quota or paused — those
// refusals are stronger and already tested above; this pins the ORDER so a
// future edit cannot let a long-elapsed floor override them.
func TestFloorDoesNotOverrideQuotaOrPause(t *testing.T) {
	now := noon()
	longAgo := now.Add(-48 * time.Hour)

	overQuota := baseInputs(now)
	overQuota.SentToday = 1_000
	overQuota.LastSentAt = longAgo
	if plan := NextDue(overQuota); plan.SendNow {
		t.Fatal("quota exhausted: SendNow must stay false however long ago the last send was")
	}

	paused := baseInputs(now)
	paused.HealthState = StatePaused
	paused.LastSentAt = longAgo
	if plan := NextDue(paused); plan.SendNow {
		t.Fatal("paused: SendNow must stay false however long ago the last send was")
	}
}
