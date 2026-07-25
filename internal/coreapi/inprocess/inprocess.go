// Package inprocess is the v1 coreapi implementation: direct in-process access
// to the database. The worker packages depend only on the coreapi.Client
// interface; this DB-backed implementation is wired at the composition root.
package inprocess

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/app/enrollment"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

type client struct {
	pool      *pgxpool.Pool
	q         *gen.Queries
	sealer    *crypto.Sealer
	jwtSecret []byte
	publicURL string
	// googleOAuth is the app's Google OAuth client config. Used to refresh a
	// gmail mailbox's access token at job-build time (see oauthAccessToken). Zero
	// value = disabled: gmail jobs then fail cleanly.
	googleOAuth mail.GoogleOAuth
	// msOAuth is the app's Microsoft (Azure AD) OAuth client config. Used to
	// refresh an m365 mailbox's access token at job-build time (see
	// oauthAccessToken). Zero value = disabled: m365 jobs then fail cleanly.
	msOAuth mail.MicrosoftOAuth
	// enroll owns the enrollment state machine (advance/complete/stop). The
	// control plane composes the domain service here so the MarkStep* coreapi
	// methods delegate the transition to a single, unit-tested place.
	enroll *enrollment.Service
}

// New returns the in-process coreapi client backed by the given connection
// pool. The pool backs the pool-bound *gen.Queries for reads and lets
// MarkStepSent run the record+advance writes in one transaction. The sealer
// decrypts stored SMTP credentials (and sealed OAuth tokens); jwtSecret signs
// stateless unsubscribe tokens; publicURL is the base URL used to build
// unsubscribe links; googleOAuth refreshes gmail mailboxes' access tokens at
// job-build time (zero value disables Gmail); msOAuth does the same for m365
// mailboxes (zero value disables Microsoft 365).
func New(pool *pgxpool.Pool, sealer *crypto.Sealer, jwtSecret []byte, publicURL string, googleOAuth mail.GoogleOAuth, msOAuth mail.MicrosoftOAuth) coreapi.Client {
	q := gen.New(pool)
	return client{
		pool: pool, q: q, sealer: sealer, jwtSecret: jwtSecret, publicURL: publicURL,
		googleOAuth: googleOAuth,
		msOAuth:     msOAuth,
		enroll:      enrollment.NewService(enrollment.NewPgStore(q)),
	}
}

// oauthConfigFor returns the provider's oauth2 config for a token refresh, or
// nil when that API provider is not configured (so oauthAccessToken fails
// cleanly). Non-API providers (smtp) have no config and never reach here.
func (c client) oauthConfigFor(provider string) *oauth2.Config {
	switch provider {
	case "gmail":
		if c.googleOAuth.Enabled() {
			return c.googleOAuth.Config()
		}
	case "m365":
		if c.msOAuth.Enabled() {
			return c.msOAuth.Config()
		}
	}
	return nil
}

func (c client) MailboxExists(ctx context.Context, id string) (bool, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return false, nil
	}
	return c.q.MailboxExists(ctx, uid)
}
