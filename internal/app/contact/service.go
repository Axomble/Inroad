package contact

import (
	"context"
	"io"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Service depends on the Store interface, never the concrete sqlc-backed
// struct (dependency inversion), plus the list service for ownership checks.
type Service struct {
	store Store
	lists *list.Service
}

func NewService(store Store, lists *list.Service) *Service { return &Service{store: store, lists: lists} }

// ImportCSV verifies the list belongs to the workspace, then imports rows.
func (s *Service) ImportCSV(ctx context.Context, ws, listID uuid.UUID, r io.Reader) (ImportResult, error) {
	if _, err := s.lists.Get(ctx, ws, listID); err != nil {
		return ImportResult{}, list.ErrNotFound
	}
	return s.importRows(ctx, ws, listID, r)
}

func (s *Service) ListByList(ctx context.Context, ws, listID uuid.UUID, limit, offset int32) ([]gen.Contact, error) {
	return s.store.ListByList(ctx, ws, listID, limit, offset)
}
