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
// confound the target/pause assertions.
func noon() time.Time {
	return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
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
