// Package list manages contact lists and membership.
package list

import (
	"context"

	"github.com/google/uuid"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so the service can
// be unit-tested against a fake without a database.
type Store interface {
	Create(ctx context.Context, workspaceID uuid.UUID, name string) (gen.List, error)
	List(ctx context.Context, workspaceID uuid.UUID) ([]gen.List, error)
	Get(ctx context.Context, workspaceID, id uuid.UUID) (gen.List, error)
	CountMembers(ctx context.Context, id uuid.UUID) (int64, error)
}

// PgStore implements Store by wrapping sqlc-generated queries.
type PgStore struct{ q *gen.Queries }

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) Create(ctx context.Context, ws uuid.UUID, name string) (gen.List, error) {
	return s.q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws, Name: name})
}
func (s *PgStore) List(ctx context.Context, ws uuid.UUID) ([]gen.List, error) {
	return s.q.ListLists(ctx, ws)
}
func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.List, error) {
	return s.q.GetList(ctx, gen.GetListParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) CountMembers(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.CountListMembers(ctx, id)
}
