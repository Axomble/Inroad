// Package inprocess is the v1 coreapi implementation: direct in-process access
// to the database. The worker packages depend only on the coreapi.Client
// interface; this DB-backed implementation is wired at the composition root.
package inprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type client struct {
	q *gen.Queries
}

// New returns the in-process coreapi client backed by the given queries.
func New(q *gen.Queries) coreapi.Client { return client{q: q} }

func (c client) MailboxExists(ctx context.Context, id string) (bool, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return false, nil
	}
	return c.q.MailboxExists(ctx, uid)
}
