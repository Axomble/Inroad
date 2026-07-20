package campaign

import (
	"context"
	"errors"

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
)

// Enqueuer schedules a send:email task for a queued send. Satisfied by
// *queue.Client; defined here so the domain doesn't depend on platform/queue.
// workspaceID travels alongside sendID so the worker can pin workspace_id
// in its DB WHERE clauses (defense in depth on top of the UUID sendID).
type Enqueuer interface {
	EnqueueSend(sendID, workspaceID string) error
}

// Service implements campaign use cases. It depends on the Store and
// Checker interfaces, not on the sqlc-backed struct or other domains'
// concrete stores -- dependency inversion.
type Service struct {
	store   Store
	checker Checker
}

func NewService(store Store, checker Checker) *Service { return &Service{store: store, checker: checker} }

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

// LaunchResult reports the outcome of a Launch call. TotalSends is the
// number of send rows the DB transaction created; EnqueuedCount and
// FailedEnqueueCount split that total by whether each id made it onto
// the queue. A non-zero FailedEnqueueCount is not a hard failure — the
// stuck-send sweeper reconciles unqueued rows on its next tick — but
// the counts are surfaced so callers can log/alert.
type LaunchResult struct {
	TotalSends         int
	EnqueuedCount      int
	FailedEnqueueCount int
}

// Launch transitions a draft campaign to running: it materializes one `sends`
// row per list member and flips the campaign status atomically (via
// store.LaunchTx), then enqueues a send:email task for every new row.
//
// Enqueue errors are counted and returned, not swallowed: the DB writes are
// already committed, so rolling back would drop legitimate work; the
// stuck-send sweeper (queue.TaskSweepStuck) re-enqueues any orphaned rows
// on the next tick.
func (s *Service) Launch(ctx context.Context, ws, campaignID uuid.UUID, enq Enqueuer) (LaunchResult, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, ErrNotFound
	}
	if c.Status != string(StatusDraft) {
		return LaunchResult{}, ErrAlreadyLaunched
	}
	ids, err := s.store.LaunchTx(ctx, ws, campaignID)
	if err != nil {
		return LaunchResult{}, err
	}
	if len(ids) == 0 {
		return LaunchResult{}, ErrEmptyList
	}
	res := LaunchResult{TotalSends: len(ids)}
	for _, id := range ids {
		if err := enq.EnqueueSend(id.String(), ws.String()); err != nil {
			res.FailedEnqueueCount++
			continue
		}
		res.EnqueuedCount++
	}
	return res, nil
}
