//go:build integration

package campaign

import (
	"context"
	"errors"
	"testing"
)

// The plan round-trips through Postgres: saved by the service, read back by the
// service, with no new-lead limit represented as NULL rather than 0 — and
// independently of whatever daily_limit is also set, since the two ceilings are
// distinct columns saved by the same PUT.
func TestMaxNewLeadsPerDayRoundTripsThroughTheSchedulePlan(t *testing.T) {
	ctx := context.Background()
	store, _, _, ws, cam := scheduleFixture(t, ctx, 1)
	svc := NewService(store, alwaysOKChecker{})

	initial, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if initial.MaxNewLeadsPerDay != nil {
		t.Errorf("a new campaign has max new leads per day %v, want none", *initial.MaxNewLeadsPerDay)
	}

	dailyLimit, newLeads := 500, 20
	if _, err := svc.SetSchedule(ctx, ws, cam.ID, Plan{
		Schedule: initial.Schedule, DailyLimit: &dailyLimit, MaxNewLeadsPerDay: &newLeads,
	}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	saved, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if saved.MaxNewLeadsPerDay == nil || *saved.MaxNewLeadsPerDay != 20 {
		t.Fatalf("max new leads per day = %v, want 20", saved.MaxNewLeadsPerDay)
	}
	// daily_limit survived the same save, since the two ceilings are independent
	// columns written by one transaction.
	if saved.DailyLimit == nil || *saved.DailyLimit != 500 {
		t.Errorf("daily limit = %v, want 500 (unaffected by the new-lead throttle)", saved.DailyLimit)
	}

	// Clearing it: an empty field on the panel means "no new-lead limit".
	if _, err := svc.SetSchedule(ctx, ws, cam.ID, Plan{Schedule: initial.Schedule, DailyLimit: &dailyLimit}); err != nil {
		t.Fatalf("SetSchedule (clear): %v", err)
	}
	cleared, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if cleared.MaxNewLeadsPerDay != nil {
		t.Errorf("max new leads per day = %v after clearing, want nil", *cleared.MaxNewLeadsPerDay)
	}
	if cleared.DailyLimit == nil || *cleared.DailyLimit != 500 {
		t.Errorf("daily limit = %v after clearing the new-lead throttle, want it untouched at 500", cleared.DailyLimit)
	}
}

// A limit below 1 is rejected before any write, so the previously saved plan
// survives — the same rule the column's CHECK enforces, surfaced as a 422 instead
// of a constraint violation.
func TestSetScheduleRejectsAnInvalidMaxNewLeadsPerDayAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	store, _, _, ws, cam := scheduleFixture(t, ctx, 1)
	svc := NewService(store, alwaysOKChecker{})

	plan, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	good := 10
	if _, err := svc.SetSchedule(ctx, ws, cam.ID, Plan{Schedule: plan.Schedule, MaxNewLeadsPerDay: &good}); err != nil {
		t.Fatalf("SetSchedule: %v", err)
	}
	for _, bad := range []int{0, -3} {
		limit := bad
		if _, err := svc.SetSchedule(ctx, ws, cam.ID, Plan{Schedule: plan.Schedule, MaxNewLeadsPerDay: &limit}); !errors.Is(err, ErrMaxNewLeadsPerDay) {
			t.Errorf("limit %d: err = %v, want ErrMaxNewLeadsPerDay", bad, err)
		}
	}
	survived, err := svc.GetSchedule(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if survived.MaxNewLeadsPerDay == nil || *survived.MaxNewLeadsPerDay != 10 {
		t.Errorf("max new leads per day = %v after rejected saves, want the previous 10", survived.MaxNewLeadsPerDay)
	}
}

// The database is the backstop: a direct write of a non-positive limit must be
// refused by the CHECK constraint, so no code path can persist one.
func TestDatabaseRejectsANonPositiveMaxNewLeadsPerDay(t *testing.T) {
	ctx := context.Background()
	_, _, pool, ws, cam := scheduleFixture(t, ctx, 1)

	for _, bad := range []int{0, -1} {
		if _, err := pool.Exec(ctx,
			`UPDATE campaigns SET max_new_leads_per_day = $3 WHERE id = $1 AND workspace_id = $2`,
			cam.ID, ws, bad); err == nil {
			t.Errorf("the database accepted max_new_leads_per_day = %d", bad)
		}
	}
}
