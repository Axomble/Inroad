package suppression

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store wraps the generated suppression queries.
type Store struct{ q *gen.Queries }

// NewStore builds a Store backed by the given generated queries.
func NewStore(q *gen.Queries) *Store { return &Store{q: q} }

// Add records a suppression entry for the given workspace/email/reason.
// Idempotent: the underlying query is ON CONFLICT DO NOTHING.
func (s *Store) Add(ctx context.Context, workspaceID uuid.UUID, email, reason string) error {
	return s.q.AddSuppression(ctx, gen.AddSuppressionParams{WorkspaceID: workspaceID, Email: email, Reason: reason})
}
