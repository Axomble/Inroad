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
type Enqueuer interface {
	EnqueueSend(sendID string) error
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

// Stats returns send counts grouped by status for the campaign.
func (s *Service) Stats(ctx context.Context, id uuid.UUID) (map[string]int64, error) {
	return s.store.Stats(ctx, id)
}

// Launch transitions a draft campaign to running: it materializes one `sends`
// row per list member, flips the campaign status, and enqueues a send:email
// task for every new row. It returns the number of sends queued.
func (s *Service) Launch(ctx context.Context, ws, campaignID uuid.UUID, enq Enqueuer) (int, error) {
	c, err := s.store.Get(ctx, ws, campaignID)
	if err != nil {
		return 0, ErrNotFound
	}
	if c.Status != string(StatusDraft) {
		return 0, ErrAlreadyLaunched
	}
	ids, err := s.store.EnqueueSends(ctx, ws, campaignID)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, ErrEmptyList
	}
	if err := s.store.SetStatus(ctx, ws, campaignID, StatusRunning); err != nil {
		return 0, err
	}
	for _, id := range ids {
		_ = enq.EnqueueSend(id.String())
	}
	return len(ids), nil
}
