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
)

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
