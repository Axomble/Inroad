package list

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/validate"
)

// ErrNotFound is returned when a list does not exist in the caller's workspace.
var ErrNotFound = errors.New("list not found")

// ErrValidation is returned when a list name fails validation.
var ErrValidation = errors.New("invalid list input")

// ErrInUse is returned when a list cannot be deleted because a campaign still
// references it (campaigns.list_id is ON DELETE RESTRICT).
var ErrInUse = errors.New("list is in use")

// Service depends on the Store interface, never the concrete sqlc-backed
// struct (dependency inversion).
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Create(ctx context.Context, ws uuid.UUID, name string) (gen.List, error) {
	return s.store.Create(ctx, ws, name)
}
func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]gen.ListListsRow, error) {
	return s.store.List(ctx, ws)
}
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.List, error) {
	return s.store.Get(ctx, ws, id)
}
func (s *Service) MemberCount(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.store.CountMembers(ctx, id)
}

// renameInput carries just the field Rename validates, so it can reuse
// validate.Struct with the exact same tag the create handler's name field uses.
type renameInput struct {
	Name string `validate:"required,min=1,max=200"`
}

// Rename replaces the list's display name. The name is validated (min=1,
// max=200, mirroring create) before the workspace-scoped update runs.
func (s *Service) Rename(ctx context.Context, ws, id uuid.UUID, name string) (gen.List, error) {
	if err := validate.Struct(renameInput{Name: name}); err != nil {
		return gen.List{}, ErrValidation
	}
	l, err := s.store.Rename(ctx, ws, id, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.List{}, ErrNotFound
	}
	return l, err
}

// Delete removes a list and (via ON DELETE CASCADE) its memberships. A list a
// campaign still references is protected by campaigns.list_id ON DELETE
// RESTRICT — that FK violation is reported as ErrInUse so the handler can
// answer 409 rather than 500.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	err := s.store.Delete(ctx, ws, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrInUse
	}
	return err
}
