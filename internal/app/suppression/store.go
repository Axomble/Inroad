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

// IsSuppressed reports whether email is on workspaceID's suppression list, a
// case-insensitive lookup backed by the (workspace_id, lower(email)) index.
// Satisfies campaign.SuppressionChecker structurally (app/* packages never
// import each other, so cmd/inroad wires this Store in directly at the
// composition root) so POST /campaigns/{id}/test-send checks the SAME
// suppression table a real send honors.
func (s *Store) IsSuppressed(ctx context.Context, workspaceID uuid.UUID, email string) (bool, error) {
	return s.q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: workspaceID, Lower: email})
}
