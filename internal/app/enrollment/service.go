package enrollment

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Service owns the enrollment state machine: the single insertion points for
// advancing the step cursor (MarkStepSent) and halting it (MarkStepStopped).
// Deferred features (reply/bounce stop, branching, business-hours pacing) hook
// in here rather than at every call site.
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

// Enroll materializes one active enrollment per list member and returns the
// new ids. The launcher stagger-schedules step 1 for each.
func (s *Service) Enroll(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	return s.store.Enroll(ctx, ws, campaignID)
}

// MarkStepSent is the single insertion point for the current_step transition
// and cadence. sentStep is the step_order just delivered; nextDueAt is when the
// following step should fire (last_sent_at + next step's delay, computed by the
// caller); lastStep true ⇒ the enrollment completes. On step 1, threadRootID
// (that step's Message-ID) is recorded once so later steps thread onto it.
func (s *Service) MarkStepSent(ctx context.Context, ws, id uuid.UUID, sentStep int32, nextDueAt time.Time, lastStep bool, threadRootID string) error {
	if sentStep == 1 && threadRootID != "" {
		if err := s.store.SetThreadRoot(ctx, ws, id, threadRootID); err != nil {
			return err
		}
	}
	if lastStep {
		return s.store.Complete(ctx, ws, id, sentStep)
	}
	return s.store.AdvanceStep(ctx, ws, id, sentStep, nextDueAt)
}

// MarkStepStopped is the single stop entry point. Unsubscribe wires it now;
// reply (StopReplied) and bounce (StopBounced) consumers call it later.
func (s *Service) MarkStepStopped(ctx context.Context, ws, id uuid.UUID, reason StopReason) error {
	return s.store.Stop(ctx, ws, id, reason)
}

// Reschedule re-stamps an active enrollment's next due time (launch stagger).
func (s *Service) Reschedule(ctx context.Context, ws, id uuid.UUID, nextDueAt time.Time) error {
	return s.store.SetDue(ctx, ws, id, nextDueAt)
}

// CountByStatus returns enrollment counts grouped by status for the campaign.
func (s *Service) CountByStatus(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error) {
	return s.store.CountByStatus(ctx, ws, campaignID)
}
