package enrollment

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on (defined by the
// consumer), backed by sqlc.
type Store interface {
	Enroll(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.SequenceEnrollment, error)
	// AdvanceStep records a successful non-final step: bumps current_step,
	// stamps last_sent_at=now(), schedules nextDueAt, stays active.
	AdvanceStep(ctx context.Context, ws, id uuid.UUID, currentStep int32, nextDueAt time.Time) error
	// Complete records the final step: bumps current_step, stamps
	// last_sent_at, marks completed, clears next_due_at.
	Complete(ctx context.Context, ws, id uuid.UUID, currentStep int32) error
	// Stop halts an active enrollment with a reason (no-op if not active).
	Stop(ctx context.Context, ws, id uuid.UUID, reason StopReason) error
	// SetDue re-stamps next_due_at for an active enrollment (launch stagger +
	// sweeper reconcile).
	SetDue(ctx context.Context, ws, id uuid.UUID, nextDueAt time.Time) error
	// SetThreadRoot stores step 1's Message-ID once (while still empty).
	SetThreadRoot(ctx context.Context, ws, id uuid.UUID, messageID string) error
	CountByStatus(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error)
}

// PgStore implements Store over the sqlc-generated queries.
type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Enroll(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}
func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.SequenceEnrollment, error) {
	return s.q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) AdvanceStep(ctx context.Context, ws, id uuid.UUID, currentStep int32, nextDueAt time.Time) error {
	return s.q.AdvanceEnrollmentStep(ctx, gen.AdvanceEnrollmentStepParams{
		ID: id, WorkspaceID: ws, CurrentStep: currentStep, NextDueAt: tsz(nextDueAt),
	})
}
func (s *PgStore) Complete(ctx context.Context, ws, id uuid.UUID, currentStep int32) error {
	return s.q.CompleteEnrollment(ctx, gen.CompleteEnrollmentParams{ID: id, WorkspaceID: ws, CurrentStep: currentStep})
}
func (s *PgStore) Stop(ctx context.Context, ws, id uuid.UUID, reason StopReason) error {
	return s.q.StopEnrollment(ctx, gen.StopEnrollmentParams{ID: id, WorkspaceID: ws, Column3: string(reason)})
}
func (s *PgStore) SetDue(ctx context.Context, ws, id uuid.UUID, nextDueAt time.Time) error {
	return s.q.SetEnrollmentDue(ctx, gen.SetEnrollmentDueParams{ID: id, WorkspaceID: ws, NextDueAt: tsz(nextDueAt)})
}
func (s *PgStore) SetThreadRoot(ctx context.Context, ws, id uuid.UUID, messageID string) error {
	return s.q.SetThreadRoot(ctx, gen.SetThreadRootParams{ID: id, WorkspaceID: ws, ThreadRootID: messageID})
}
func (s *PgStore) CountByStatus(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.CountEnrollmentsByStatus(ctx, gen.CountEnrollmentsByStatusParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out, nil
}

func tsz(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
