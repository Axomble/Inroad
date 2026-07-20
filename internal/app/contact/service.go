package contact

import (
	"context"
	"errors"
	"io"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrListNotFound is what the handler layer maps to 404 when the target
// list doesn't belong to the caller's workspace. Kept in this package so
// no other domain has to be imported (mirrors campaign.Checker).
var ErrListNotFound = errors.New("list not found")

// ListChecker validates that a list belongs to the caller's workspace,
// without this domain having to know about the list domain (that would
// break the "app packages don't import each other" invariant in
// docs/architecture.md). The composition root in cmd/inroad wires a small
// adapter over *list.Service (mirrors campaign.Checker).
type ListChecker interface {
	ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error)
}

// Service depends on the Store interface (never the concrete sqlc-backed
// struct — dependency inversion) plus a ListChecker for cross-domain
// ownership checks.
type Service struct {
	store   Store
	checker ListChecker
}

// NewService constructs a Service backed by store and using checker to
// verify list ownership before mutating imports.
func NewService(store Store, checker ListChecker) *Service {
	return &Service{store: store, checker: checker}
}

// ImportCSV verifies the list belongs to the workspace, then imports rows.
func (s *Service) ImportCSV(ctx context.Context, ws, listID uuid.UUID, r io.Reader) (ImportResult, error) {
	ok, err := s.checker.ListExists(ctx, ws, listID)
	if err != nil {
		return ImportResult{}, err
	}
	if !ok {
		return ImportResult{}, ErrListNotFound
	}
	return s.importRows(ctx, ws, listID, r)
}

func (s *Service) ListByList(ctx context.Context, ws, listID uuid.UUID, limit, offset int32) ([]gen.Contact, error) {
	return s.store.ListByList(ctx, ws, listID, limit, offset)
}
