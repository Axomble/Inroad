// Package inprocess is the v1 coreapi implementation: direct in-process access
// to the database. The worker packages depend only on the coreapi.Client
// interface; this DB-backed implementation is wired at the composition root.
package inprocess

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/app/deliverability"
	"github.com/inroad/inroad/internal/app/enrollment"
	"github.com/inroad/inroad/internal/app/idempotency"
	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/warmup"
)

type client struct {
	pool      *pgxpool.Pool
	q         *gen.Queries
	keyring   *crypto.Keyring
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
	// warmupSecret signs the X-Inroad-Warmup receipt token on every warmup send,
	// mirroring the tracking-secret discipline. Injected (never a package global) so
	// the signing key is a composition-root decision.
	warmupSecret []byte
	// warmupContent is the injected content library (ContentGenerator seam) that
	// produces the synthetic conversations warmup sends carry. The static library is
	// the v1 impl; an AI generator drops in behind the same interface.
	warmupContent warmup.ContentGenerator
	// breaker owns the campaign circuit breaker (score + verdict + the pause
	// transition). Composed here for the same reason as enroll: the worker reaches
	// it through EvaluateCampaignBreaker, so there is exactly one implementation and
	// the API and the execution plane cannot disagree about when a campaign stops.
	breaker *deliverability.Service
	// inbox owns unified-inbox thread/message storage. Composed here (needs
	// nothing beyond pool, like enroll/breaker above) so the inbox poller's
	// StoreInboundMessage writes through the SAME transactional
	// Service.RecordReply the control-plane HTTP handler reads from, rather than
	// re-deriving the upsert-thread-then-insert-message atomicity here.
	inbox *inbox.Service
	// replyClaims backs ClaimInboxReply/ReleaseInboxReply's claim-before-send
	// guard (worker/inbox.ReplyCore) — a DELIBERATE reuse of the generic HTTP
	// Idempotency-Key replay cache (migration 000045): right shape (claim a
	// key once, release it on a retryable failure), right retention (rows
	// age out via the SAME 24h maintenance sweep, idempotency_keys needs no
	// dedicated one), no new schema for what is structurally the identical
	// problem (see ClaimInboxReply's own doc).
	replyClaims *idempotency.PgStore
	// now is the client's clock, injected rather than read from time.Now() at each
	// call site. The warmup scheduler's day shape is a function of the CALENDAR DAY
	// (warmup.EffectiveDailyVolume drops weekends hard and skips ~4% of weekdays
	// outright), so a warmup path that reads the wall clock directly cannot be
	// integration-tested deterministically — the same test passes on a Tuesday and
	// fails on a Saturday. Mirrors deliverability.Service.now, for the same reason.
	// New defaults it to time.Now; only tests replace it.
	now func() time.Time
}

// New returns the in-process coreapi client backed by the given connection
// pool. The pool backs the pool-bound *gen.Queries for reads and lets
// MarkStepSent run the record+advance writes in one transaction. The keyring
// resolves a per-workspace Sealer that decrypts stored SMTP credentials (and
// sealed OAuth tokens) and re-seals rotated tokens; jwtSecret signs stateless
// unsubscribe tokens; publicURL is the base URL used to build unsubscribe
// links; googleOAuth refreshes gmail mailboxes' access tokens at job-build time
// (zero value disables Gmail); msOAuth does the same for m365 mailboxes (zero
// value disables Microsoft 365); warmupSecret signs the warmup receipt token and
// warmupContent is the injected warmup content library (both used only by the
// warmup send path).
func New(pool *pgxpool.Pool, keyring *crypto.Keyring, jwtSecret []byte, publicURL string, googleOAuth mail.GoogleOAuth, msOAuth mail.MicrosoftOAuth, warmupSecret []byte, warmupContent warmup.ContentGenerator) coreapi.Client {
	q := gen.New(pool)
	return client{
		pool: pool, q: q, keyring: keyring, jwtSecret: jwtSecret, publicURL: publicURL,
		googleOAuth:   googleOAuth,
		msOAuth:       msOAuth,
		enroll:        enrollment.NewService(enrollment.NewPgStore(q)),
		breaker:       deliverability.NewService(deliverability.NewPgStore(pool)),
		warmupSecret:  warmupSecret,
		warmupContent: warmupContent,
		inbox:         inbox.NewService(inbox.NewPgStore(pool)),
		replyClaims:   idempotency.NewPgStore(pool),
		now:           time.Now,
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
		//nolint:nilerr // a malformed id can't identify any mailbox: absent, not a lookup failure
		return false, nil
	}
	return c.q.MailboxExists(ctx, uid)
}
