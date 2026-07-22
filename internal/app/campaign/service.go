package campaign

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Sentinel errors the handler layer maps to HTTP status codes.
var (
	ErrNotFound         = errors.New("campaign not found")
	ErrMailboxNotActive = errors.New("mailbox not found or not active")
	ErrListMissing      = errors.New("list not found")
	ErrValidation       = errors.New("invalid campaign input")
	ErrAlreadyLaunched  = errors.New("campaign already launched")
	ErrEmptyList        = errors.New("target list is empty")
	ErrNoSteps          = errors.New("campaign has no sequence steps")
)

// Enqueuer schedules a sequence:advance task at a given time. Satisfied by
// *queue.Client; defined here so the domain doesn't depend on platform/queue.
// workspaceID travels alongside enrollmentID so the worker can pin workspace_id
// in its DB WHERE clauses (defense in depth on top of the UUID enrollmentID).
type Enqueuer interface {
	EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error
}

// Service implements campaign use cases. It depends on the Store and
// Checker interfaces, not on the sqlc-backed struct or other domains'
// concrete stores -- dependency inversion.
type Service struct {
	store   Store
	checker Checker
}

func NewService(store Store, checker Checker) *Service {
	return &Service{store: store, checker: checker}
}

// Create verifies the mailbox is active and the list exists in the
// workspace before persisting the campaign.
func (s *Service) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error) {
	active, err := s.checker.MailboxActive(ctx, ws, in.MailboxID)
	if err != nil {
		return gen.Campaign{}, err
	}
	if !active {
		return gen.Campaign{}, ErrMailboxNotActive
	}
	exists, err := s.checker.ListExists(ctx, ws, in.ListID)
	if err != nil {
		return gen.Campaign{}, err
	}
	if !exists {
		return gen.Campaign{}, ErrListMissing
	}
	return s.store.Create(ctx, ws, in)
}

// Get returns a single campaign, scoped to the workspace.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	return s.store.Get(ctx, ws, id)
}

// List returns every campaign in the workspace.
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error) {
	return s.store.List(ctx, ws)
}

// Stats returns send counts grouped by status for the campaign. The
// workspace id is included so a cross-tenant campaign id yields empty
// results rather than leaking counts (defense in depth on top of the
// ownership check the caller has already run via Get).
func (s *Service) Stats(ctx context.Context, ws, id uuid.UUID) (map[string]int64, error) {
	return s.store.Stats(ctx, ws, id)
}

// CampaignDetail is the extended GET /campaigns/{id} payload: the campaign, its
// ordered steps, send counts by status, and enrollment counts by status.
type CampaignDetail struct {
	Campaign    gen.Campaign
	Steps       []gen.SequenceStep
	SendStats   map[string]int64
	Enrollments map[string]int64
}

// Detail loads the campaign plus its steps and rollup counts, all
// workspace-scoped (a cross-tenant id yields ErrNotFound before any child read).
func (s *Service) Detail(ctx context.Context, ws, id uuid.UUID) (CampaignDetail, error) {
	c, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, ErrNotFound
	}
	steps, err := s.store.ListSteps(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, err
	}
	sends, err := s.store.Stats(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, err
	}
	enr, err := s.store.EnrollmentCounts(ctx, ws, id)
	if err != nil {
		return CampaignDetail{}, err
	}
	return CampaignDetail{Campaign: c, Steps: steps, SendStats: sends, Enrollments: enr}, nil
}

// LaunchResult reports the outcome of a Launch call. TotalEnrolled is the
// number of enrollments the DB transaction created; EnqueuedCount and
// FailedEnqueueCount split that total by whether each enrollment's step-1
// advance made it onto the queue. A non-zero FailedEnqueueCount is not a hard
// failure — the enrollment sweeper reconciles unqueued rows on its next tick —
// but the counts are surfaced so callers can log/alert.
type LaunchResult struct {
	TotalEnrolled      int
	EnqueuedCount      int
	FailedEnqueueCount int
}

// Launch transitions a draft campaign to running: it materializes one
// enrollment per list member and flips the campaign status atomically (via
// store.EnrollTx), then stagger-schedules a sequence:advance task for every new
// enrollment (setting its next_due_at to match, so the sweeper won't fire it
// early). The lazy chain enqueues each subsequent step after the prior sends.
//
// Enqueue errors are counted, not swallowed: the DB writes are already
// committed, so rolling back would drop legitimate work; the enrollment sweeper
// (queue.TaskSweepEnrollments) re-enqueues any orphaned enrollments next tick.
func (s *Service) Launch(ctx context.Context, ws, campaignID uuid.UUID, enq Enqueuer) (LaunchResult, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, ErrNotFound
	}
	if c.Status != string(StatusDraft) {
		return LaunchResult{}, ErrAlreadyLaunched
	}
	steps, err := s.store.CountSteps(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, err
	}
	if steps == 0 {
		return LaunchResult{}, ErrNoSteps
	}
	enrollments, err := s.store.EnrollTx(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, err
	}
	if len(enrollments) == 0 {
		return LaunchResult{}, ErrEmptyList
	}
	res := LaunchResult{TotalEnrolled: len(enrollments)}
	for _, e := range enrollments {
		// EnrollListMembers already staggered next_due_at at insert time; we
		// enqueue each advance at exactly that DB-assigned time (asynq needs one
		// task per enrollment) so the scheduled task and the enrollment's due
		// cursor are identical by construction — never recompute the stagger in
		// Go, since RETURNING row order isn't guaranteed to match the window
		// ORDER BY. A failed enqueue is non-fatal — the enrollment sweeper
		// reconciles it next tick.
		if err := enq.EnqueueAdvanceAt(e.ID.String(), ws.String(), e.NextDueAt); err != nil {
			res.FailedEnqueueCount++
			continue
		}
		res.EnqueuedCount++
	}
	return res, nil
}
