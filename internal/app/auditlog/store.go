// Package auditlog is the READ side of the workspace audit log: the
// owner/admin viewer at GET /api/v1/audit-events. Events are written by every
// domain through internal/platform/audit; this package never writes one.
//
// Named auditlog rather than audit so a domain that both records (platform)
// and — in a composition root — mounts the viewer (app) never has to alias one
// of two identically named imports.
package auditlog

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store is the persistence seam the service depends on: one keyset-paged,
// workspace-pinned read.
type Store interface {
	List(ctx context.Context, p gen.ListAuditEventsParams) ([]gen.ListAuditEventsRow, error)
}

// PgStore is the sqlc-backed Store.
type PgStore struct{ q *gen.Queries }

// NewPgStore builds a PgStore over pool.
func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{q: gen.New(pool)} }

// List implements Store.
func (s *PgStore) List(ctx context.Context, p gen.ListAuditEventsParams) ([]gen.ListAuditEventsRow, error) {
	return s.q.ListAuditEvents(ctx, p)
}
