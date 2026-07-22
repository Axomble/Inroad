// Package inprocess is the v1 coreapi implementation: direct in-process access
// to the database. The worker packages depend only on the coreapi.Client
// interface; this DB-backed implementation is wired at the composition root.
package inprocess

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/enrollment"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type client struct {
	pool      *pgxpool.Pool
	q         *gen.Queries
	sealer    *crypto.Sealer
	jwtSecret []byte
	publicURL string
	// enroll owns the enrollment state machine (advance/complete/stop). The
	// control plane composes the domain service here so the MarkStep* coreapi
	// methods delegate the transition to a single, unit-tested place.
	enroll *enrollment.Service
}

// New returns the in-process coreapi client backed by the given connection
// pool. The pool backs the pool-bound *gen.Queries for reads and lets
// MarkStepSent run the record+advance writes in one transaction. The sealer
// decrypts stored SMTP credentials; jwtSecret signs stateless unsubscribe
// tokens; publicURL is the base URL used to build unsubscribe links.
func New(pool *pgxpool.Pool, sealer *crypto.Sealer, jwtSecret []byte, publicURL string) coreapi.Client {
	q := gen.New(pool)
	return client{
		pool: pool, q: q, sealer: sealer, jwtSecret: jwtSecret, publicURL: publicURL,
		enroll: enrollment.NewService(enrollment.NewPgStore(q)),
	}
}

func (c client) MailboxExists(ctx context.Context, id string) (bool, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return false, nil
	}
	return c.q.MailboxExists(ctx, uid)
}
