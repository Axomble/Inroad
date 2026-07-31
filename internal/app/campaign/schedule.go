package campaign

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cadence"
)

// Schedule and SendWindow are the cadence types re-exported under the domain's
// name: one definition, one validation path, shared by the app layer and the
// coreapi seam. Derived rather than re-declared so a shape change can't drift
// between the two.
type (
	Schedule   = cadence.Schedule
	SendWindow = cadence.SendWindow
)

// DefaultSchedule is the schedule a new campaign is created with.
func DefaultSchedule(tz string) Schedule { return cadence.DefaultSchedule(tz) }

// ErrDailyLimit rejects a campaign daily limit outside the column's range, mapped
// to 422 by the handler. Mirrors the campaigns.daily_limit CHECK, so an invalid
// value is a 422 rather than a constraint violation surfacing as a 500. "No limit"
// is expressed by a nil limit, never by zero.
var ErrDailyLimit = errors.New("daily limit out of range")

// Plan is a campaign's whole sending plan: when it may send (the schedule) and at
// most how much per UTC day (DailyLimit, nil = no campaign limit). One panel and
// one save own both, so the service reads and writes them together — a schedule
// with a stale limit beside it is a plan that was never configured as a whole.
//
// The limit is campaign-WIDE across the sender pool, and can only lower throughput:
// it never raises a mailbox above its own ramped, health-scaled cap.
type Plan struct {
	Schedule
	DailyLimit *int
}

// GetSchedule returns the campaign's sending plan: timezone, weekly windows and
// campaign-wide daily limit.
func (s *Service) GetSchedule(ctx context.Context, ws, campaignID uuid.UUID) (Plan, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return Plan{}, ErrNotFound
	}
	windows, err := s.store.ListWindows(ctx, ws, campaignID)
	if err != nil {
		return Plan{}, err
	}
	// A campaign with no window rows was never configured (a direct insert, the
	// seeder) rather than corrupted, so it reads as the default schedule.
	return Plan{Schedule: cadence.ScheduleFrom(c.Timezone, windows), DailyLimit: dailyLimit(c.DailyLimit)}, nil
}

// SetSchedule replaces the campaign's sending plan wholesale. Validation runs
// before any write, so a rejected plan leaves the previous one intact.
//
// Editable while the campaign is running, like SetTracking: a plan change only
// affects sends scheduled after it. Enrollments already carrying a next_due_at keep
// it — re-stamping work in flight would silently reshuffle a running campaign, so
// it is deliberately not done here. A lowered daily limit likewise does not recall
// today's already-sent volume; it takes effect from the next step that asks.
func (s *Service) SetSchedule(ctx context.Context, ws, campaignID uuid.UUID, in Plan) (Plan, error) {
	if _, err := s.store.Get(ctx, ws, campaignID); err != nil {
		return Plan{}, ErrNotFound
	}
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	if in.DailyLimit != nil && (*in.DailyLimit < minDailyLimit || *in.DailyLimit > maxDailyLimit) {
		return Plan{}, ErrDailyLimit
	}
	// Compile is the validation: unknown zone, malformed interval, overlap, or an
	// all-closed week is rejected before anything is persisted.
	if _, err := in.Compile(); err != nil {
		return Plan{}, err
	}
	if err := s.store.ReplaceSchedule(ctx, ws, campaignID, in); err != nil {
		return Plan{}, err
	}
	return in, nil
}

// minDailyLimit is the smallest meaningful campaign-wide daily limit. Zero is not
// "no limit" — that is nil — it would be "never send", which an operator expresses
// by pausing the campaign.
//
// maxDailyLimit mirrors the OpenAPI contract's maximum. Bounded here rather than
// left to the INT column: a value like 99999999999 is a well-formed JSON number
// that reaches Postgres out of range, which surfaces as a 500 instead of the 422
// this is. The client's own bound is a convenience — this is the guarantee.
const (
	minDailyLimit = 1
	maxDailyLimit = 1_000_000
)

// dailyLimit converts the nullable column into the optional Go value, so "no
// limit" is nil rather than a zero that reads as a limit of none-per-day.
func dailyLimit(stored *int32) *int {
	if stored == nil {
		return nil
	}
	n := int(*stored)
	return &n
}

// window loads and compiles the campaign's schedule for a scheduling decision.
func (s *Service) window(ctx context.Context, ws uuid.UUID, c campaignSchedule) (cadence.Window, error) {
	windows, err := s.store.ListWindows(ctx, ws, c.id)
	if err != nil {
		return cadence.Window{}, err
	}
	return cadence.ScheduleFrom(c.timezone, windows).Compile()
}

// campaignSchedule is the slice of a campaign the scheduler needs, so window()
// doesn't take a whole gen.Campaign to read two fields from.
type campaignSchedule struct {
	id       uuid.UUID
	timezone string
}
