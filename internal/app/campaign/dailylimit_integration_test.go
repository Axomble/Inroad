//go:build integration

package campaign

import (
	"context"
	"errors"
	"testing"

	"github.com/inroad/inroad/internal/platform/sendcap"
)

// The plan round-trips through Postgres: saved by the service, read back by the
// service, with no campaign limit represented as NULL rather than 0.
func TestDailyLimitRoundTripsThroughTheSchedulePlan(t *testing.T) {
	ctx := context.Background()
	store, _, _, ws, cam := scheduleFixture(t, ctx, 1)
	svc := NewService(store, alwaysOKChecker{})

	initial, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if initial.DailyLimit != nil {
		t.Errorf("a new campaign has daily limit %v, want none", *initial.DailyLimit)
	}

	limit := 120
	if _, err := svc.SetSchedule(ctx, ws, cam.ID, Plan{Schedule: initial.Schedule, DailyLimit: &limit}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	saved, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if saved.DailyLimit == nil || *saved.DailyLimit != 120 {
		t.Fatalf("daily limit = %v, want 120", saved.DailyLimit)
	}
	// The windows survived the same save, since the plan is written as one unit.
	if len(saved.Windows) != len(initial.Windows) {
		t.Errorf("windows = %d, want the original %d", len(saved.Windows), len(initial.Windows))
	}

	// Clearing it: an empty field on the panel means "no campaign limit".
	if _, err := svc.SetSchedule(ctx, ws, cam.ID, Plan{Schedule: initial.Schedule}); err != nil {
		t.Fatalf("SetSchedule (clear): %v", err)
	}
	cleared, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if cleared.DailyLimit != nil {
		t.Errorf("daily limit = %v after clearing, want nil", *cleared.DailyLimit)
	}
}

// A limit below 1 is rejected before any write, so the previously saved plan
// survives — the same rule the column's CHECK enforces, surfaced as a 422 instead of
// a constraint violation.
func TestSetScheduleRejectsAnInvalidLimitAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	store, _, _, ws, cam := scheduleFixture(t, ctx, 1)
	svc := NewService(store, alwaysOKChecker{})

	plan, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	good := 50
	if _, err := svc.SetSchedule(ctx, ws, cam.ID, Plan{Schedule: plan.Schedule, DailyLimit: &good}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	for _, bad := range []int{0, -3} {
		limit := bad
		if _, err := svc.SetSchedule(ctx, ws, cam.ID, Plan{Schedule: plan.Schedule, DailyLimit: &limit}); !errors.Is(err, ErrDailyLimit) {
			t.Errorf("limit %d: err = %v, want ErrDailyLimit", bad, err)
		}
	}
	survived, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if survived.DailyLimit == nil || *survived.DailyLimit != 50 {
		t.Errorf("daily limit = %v after rejected saves, want the previous 50", survived.DailyLimit)
	}
}

// The database is the backstop: a direct write of a non-positive limit must be
// refused by the CHECK constraint, so no code path can persist one.
func TestDatabaseRejectsANonPositiveDailyLimit(t *testing.T) {
	ctx := context.Background()
	_, _, pool, ws, cam := scheduleFixture(t, ctx, 1)

	for _, bad := range []int{0, -1} {
		if _, err := pool.Exec(ctx,
			`UPDATE campaigns SET daily_limit = $3 WHERE id = $1 AND workspace_id = $2`,
			cam.ID, ws, bad); err == nil {
			t.Errorf("the database accepted daily_limit = %d", bad)
		}
	}
}

// The senders panel must report the cap the send path will actually enforce. This is
// the read half of health gating: a paused mailbox reports sending=false with a cap
// of 0, a throttled one reports its halved cap, and both come from the same
// platform/sendcap arithmetic the sender uses.
func TestSendersReportHealthAndTodaysCapacity(t *testing.T) {
	ctx := context.Background()
	store, _, pool, ws, cam := scheduleFixture(t, ctx, 1)
	svc := NewService(store, alwaysOKChecker{})

	// The campaign has no pool rows, so this exercises the fallback projection too.
	senders, err := svc.GetSenders(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if len(senders.Senders) != 1 {
		t.Fatalf("senders = %d, want the 1-mailbox fallback", len(senders.Senders))
	}
	only := senders.Senders[0]
	if only.HealthState != nil {
		t.Errorf("health_state = %q, want null for a mailbox that is not warming up", *only.HealthState)
	}
	if !only.Sending || only.CapToday != 500 || only.SentToday != 0 {
		t.Errorf("sender = {sending:%v cap:%d sent:%d}, want a sending mailbox at its full cap of 500",
			only.Sending, only.CapToday, only.SentToday)
	}

	for _, tc := range []struct {
		state       string
		wantCap     int
		wantSending bool
	}{
		{sendcap.HealthWatch, 350, true},
		{sendcap.HealthThrottled, 250, true},
		{sendcap.HealthPaused, 0, false},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO warmup_participants (mailbox_id, workspace_id, health_state)
			 VALUES ($1,$2,$3) ON CONFLICT (mailbox_id) DO UPDATE SET health_state = EXCLUDED.health_state`,
			only.MailboxID, ws, tc.state); err != nil {
			t.Fatalf("warmup participant %s: %v", tc.state, err)
		}
		got, err := svc.GetSenders(ctx, ws, cam.ID)
		if err != nil {
			t.Fatalf("GetSenders: %v", err)
		}
		row := got.Senders[0]
		if row.HealthState == nil || *row.HealthState != tc.state {
			t.Errorf("health_state = %v, want %q", row.HealthState, tc.state)
		}
		if row.CapToday != tc.wantCap {
			t.Errorf("%s: cap_today = %d, want %d", tc.state, row.CapToday, tc.wantCap)
		}
		if row.Sending != tc.wantSending {
			t.Errorf("%s: sending = %v, want %v", tc.state, row.Sending, tc.wantSending)
		}
	}

	// Warmup off means no live health signal, so the row reports no health and its
	// full cap — nothing would ever clear a frozen 'paused'.
	if _, err := pool.Exec(ctx,
		`UPDATE warmup_participants SET enabled = false WHERE mailbox_id = $1`, only.MailboxID); err != nil {
		t.Fatalf("disable warmup: %v", err)
	}
	got, err := svc.GetSenders(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if row := got.Senders[0]; row.HealthState != nil || !row.Sending || row.CapToday != 500 {
		t.Errorf("disabled participant reported {health:%v sending:%v cap:%d}, want no health signal at full cap",
			row.HealthState, row.Sending, row.CapToday)
	}
}
