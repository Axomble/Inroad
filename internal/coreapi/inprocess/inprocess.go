// Package inprocess is the v1 coreapi implementation: direct in-process access
// to the database. The worker packages depend only on the coreapi.Client
// interface; this DB-backed implementation is wired at the composition root.
package inprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type client struct {
	q         *gen.Queries
	sealer    *crypto.Sealer
	jwtSecret []byte
	publicURL string
}

// New returns the in-process coreapi client backed by the given queries. The
// sealer decrypts stored SMTP credentials; jwtSecret signs stateless
// unsubscribe tokens; publicURL is the base URL used to build unsubscribe
// links.
func New(q *gen.Queries, sealer *crypto.Sealer, jwtSecret []byte, publicURL string) coreapi.Client {
	return client{q: q, sealer: sealer, jwtSecret: jwtSecret, publicURL: publicURL}
}

func (c client) MailboxExists(ctx context.Context, id string) (bool, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return false, nil
	}
	return c.q.MailboxExists(ctx, uid)
}
