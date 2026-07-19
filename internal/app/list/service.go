package list

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrNotFound is returned when a list does not exist in the caller's workspace.
var ErrNotFound = errors.New("list not found")

// Service depends on the Store interface, never the concrete sqlc-backed
// struct (dependency inversion).
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Create(ctx context.Context, ws uuid.UUID, name string) (gen.List, error) {
	return s.store.Create(ctx, ws, name)
}
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]gen.List, error) {
	return s.store.List(ctx, ws)
}
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.List, error) {
	return s.store.Get(ctx, ws, id)
}
func (s *Service) MemberCount(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.store.CountMembers(ctx, id)
}
