// Package reporting serves the workspace-wide, cross-campaign performance
// rollup behind GET /reports/campaigns. Read-only — this domain owns no table
// and writes nothing, like pulse.
package reporting

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the repository interface this domain depends on, defined here (by
// the consumer) so the service unit-tests against a fake without a database.
// Workspace-pinned, like every tenant-scoped read.
type Store interface {
	CampaignPerformance(ctx context.Context, workspaceID uuid.UUID) ([]gen.ListCampaignPerformanceRow, error)
}

// PgStore implements Store by delegating to the sqlc-generated query; it is the
// only place in this domain that knows about gen.Queries.
type PgStore struct {
	q *gen.Queries
}

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) CampaignPerformance(ctx context.Context, workspaceID uuid.UUID) ([]gen.ListCampaignPerformanceRow, error) {
	return s.q.ListCampaignPerformance(ctx, workspaceID)
}
