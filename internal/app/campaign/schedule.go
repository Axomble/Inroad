package campaign

import (
	"context"

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

// GetSchedule returns the campaign's timezone and weekly windows.
func (s *Service) GetSchedule(ctx context.Context, ws, campaignID uuid.UUID) (Schedule, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return Schedule{}, ErrNotFound
	}
	windows, err := s.store.ListWindows(ctx, ws, campaignID)
	if err != nil {
		return Schedule{}, err
	}
	// A campaign with no window rows was never configured (a direct insert, the
	// seeder) rather than corrupted, so it reads as the default schedule.
	return cadence.ScheduleFrom(c.Timezone, windows), nil
}

// SetSchedule replaces the campaign's schedule wholesale. Validation runs before
// any write, so a rejected schedule leaves the previous one intact.
//
// Editable while the campaign is running, like SetTracking: a schedule change
// only affects sends scheduled after it. Enrollments already carrying a
// next_due_at keep it — re-stamping work in flight would silently reshuffle a
// running campaign, so it is deliberately not done here.
func (s *Service) SetSchedule(ctx context.Context, ws, campaignID uuid.UUID, in Schedule) (Schedule, error) {
	if _, err := s.store.Get(ctx, ws, campaignID); err != nil {
		return Schedule{}, ErrNotFound
	}
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	// Compile is the validation: unknown zone, malformed interval, overlap, or an
	// all-closed week is rejected before anything is persisted.
	if _, err := in.Compile(); err != nil {
		return Schedule{}, err
	}
	if err := s.store.ReplaceSchedule(ctx, ws, campaignID, in); err != nil {
		return Schedule{}, err
	}
	return in, nil
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
