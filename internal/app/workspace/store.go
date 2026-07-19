// Package workspace is the tenant root: workspaces and their member users.
package workspace

import (
	"context"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store wraps sqlc queries this domain needs. Data access lives inside the domain.
type Store struct {
	q *gen.Queries
}

func NewStore(q *gen.Queries) *Store { return &Store{q: q} }

func (s *Store) CreateWorkspace(ctx context.Context, name string) (gen.Workspace, error) {
	return s.q.CreateWorkspace(ctx, name)
}

func (s *Store) CreateUser(ctx context.Context, arg gen.CreateUserParams) (gen.User, error) {
	return s.q.CreateUser(ctx, arg)
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (gen.User, error) {
	return s.q.GetUserByEmail(ctx, email)
}
