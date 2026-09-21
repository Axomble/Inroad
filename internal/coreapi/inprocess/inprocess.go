// Package inprocess is the v1 coreapi implementation: direct in-process access
// to the database. The worker packages depend only on the coreapi.Client
// interface; this DB-backed implementation is wired at the composition root.
package inprocess

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/deliverability"
	"github.com/inroad/inroad/internal/app/enrollment"
	"github.com/inroad/inroad/internal/app/idempotency"
	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/app/webhook"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/realtime"
	"github.com/inroad/inroad/internal/platform/warmup"
)

type client struct {
	pool *pgxpool.Pool
	q    *gen.Queries
	// creds opens stored secrets. It is the ONLY route from this client to a
	// decrypted credential — the client itself holds no key. In the control
	// plane (and in the single-process self-host worker) it is the
	// keyring-backed localOpener; in a fleet worker it is credbroker's HTTP
	// client, so that process never sees INROAD_MASTER_KEY at all. See
	// internal/platform/credbroker for why that boundary exists and what it
	// does and does not buy.
	creds credbroker.Opener
	// suppression answers IsSuppressed. Like creds above, it is the ONLY route
	// from this client to that answer, so replacing it moves every consumer at
	// once. Defaults to localSuppression (the pool-backed query every
	// self-hosted install runs). See suppression.go, and the note on
	// WithRemoteSuppression for why nothing in production replaces it any more.
	suppression SuppressionSource
	// jobs answers the per-message job reads. NIL IS THE DEFAULT and means
	// "build them here, from the pool" — the self-host path, unchanged. See
	// jobsource.go for why this one defaults to nil where suppression defaults
	// to a value, and WithRemoteJobs for why nothing replaces it any more.
	jobs JobSource
	// outcomes accepts the claim and outcome writes. NIL IS THE DEFAULT and
	// means "write them here, to the pool" — the self-host path, unchanged.
	// Same nil-means-local shape as jobs above; see outcomesource.go.
	outcomes OutcomeSource
	// inboxSends answers the manual reply/compose protocol — the mail a HUMAN
	// wrote. NIL IS THE DEFAULT and means "here, through the pool" — the
	// self-host path, unchanged. Same nil-means-local shape as jobs and outcomes
	// above; see inboxsendsource.go.
	inboxSends InboxSendSource
	jwtSecret  []byte
	publicURL  string
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
	// realtime fans events out to a workspace's connected browsers. NIL IS
	// VALID and means "no realtime": PublishRealtime becomes a no-op and clients
	// fall back to polling. Injected rather than dialled here so the API and
	// worker processes share one hub configuration decided at their composition
	// roots.
	realtime realtime.Publisher
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
	// webhookEmitter fans domain events (reply.received, email.bounced,
	// contact.unsubscribed) out to a workspace's registered webhook endpoints.
	// NIL IS VALID and means "webhooks disabled": emitWebhook becomes a no-op.
	// Injected here rather than dialled because Dispatch needs the pool and the
	// queue client, which the composition root already holds.
	webhookEmitter webhook.Emitter
	// now is the client's clock, injected rather than read from time.Now() at each
	// call site. The warmup scheduler's day shape is a function of the CALENDAR DAY
	// (warmup.EffectiveDailyVolume drops weekends hard and skips ~4% of weekdays
	// outright), so a warmup path that reads the wall clock directly cannot be
	// integration-tested deterministically — the same test passes on a Tuesday and
	// fails on a Saturday. Mirrors deliverability.Service.now, for the same reason.
	// New defaults it to time.Now; only tests replace it.
	now func() time.Time
	// mtx records claim-before-send outcomes (inroad_send_claims_total) from the
	// one place every outcome is already branched on — the claim itself — so a
	// lost or reclaimed lease is counted even on paths the calling worker never
	// distinguishes. nil (the default, and what every test and cmd/seed gets) is
	// a no-op: *metrics.Metrics is nil-receiver-safe throughout.
	mtx *metrics.Metrics
}

// Option customises the in-process client at its composition root. It exists
// so an optional cross-cutting concern (metrics today) can be wired without a
// ninth positional parameter on New, and without every test and every other
// caller having to pass a zero value for something they do not use.
type Option func(*client)

// WithMetrics wires the process's Prometheus metrics into the client, so the
// claim-before-send path reports won/lost/reclaimed outcomes. Passing a nil
// *metrics.Metrics is valid and equivalent to omitting the option.
func WithMetrics(mtx *metrics.Metrics) Option {
	return func(c *client) { c.mtx = mtx }
}

// WithRealtime wires the realtime fan-out hub in, enabling PublishRealtime.
//
// Optional on purpose. Omitting it (or passing nil) leaves the client without
// the capability, and every publish becomes a no-op: browsers then learn about a
// change on their next poll or refetch, which is exactly today's behaviour. A
// socket event is an optimisation over polling, never the only path, so a worker
// without a hub still does all of its real work.
func WithRealtime(pub realtime.Publisher) Option {
	return func(c *client) { c.realtime = pub }
}

// WithWebhooks wires the outbound-webhook emitter in, enabling reply.received /
// email.bounced / contact.unsubscribed fan-out. Omitting it (or passing nil)
// leaves the client without the capability and every emit is a no-op.
func WithWebhooks(e webhook.Emitter) Option {
	return func(c *client) { c.webhookEmitter = e }
}

// WithCredentialBroker replaces the keyring-backed credential opener with
// another credbroker.Opener — in practice credbroker's HTTP client, pointed at
// the control plane.
//
// This is what lets a fleet worker run with NO master key: pass a nil keyring
// to New and this option, and the process holds nothing that can unwrap a
// workspace DEK. It is an Option rather than a positional parameter because
// every other caller (cmd/inroad, cmd/seed, every test, and the self-host
// RoleAll worker) wants the local opener and should not have to say so.
//
// Passing nil is a no-op, NOT a way to disable credentials: silently dropping
// a broker that was meant to be wired would leave the process opening secrets
// with a local keyring it was supposed to have given up.
func WithCredentialBroker(o credbroker.Opener) Option {
	return func(c *client) {
		if o != nil {
			c.creds = o
		}
	}
}

// WithRemoteSuppression replaces the pool-backed suppression query with another
// SuppressionSource — in practice internal/coreapi/remote's HTTP client.
//
// THESE FOUR OPTIONS NOW HAVE NO PRODUCTION CALLER, and that is worth saying
// once, here, rather than four times. They were how slices 1–3b moved the
// boundary a method group at a time while cmd/worker still built an in-process
// client for every role. Slice 4 finished the move: a role=send worker uses
// *remote.Client WHOLE and builds no in-process client at all, and every other
// role uses the local path — so there is no longer a caller that wants half of
// each. What still exercises them is the fault-injection integration suite in
// this package, which proves the never-double-send properties over a hybrid;
// migrating those to drive *remote.Client directly is the follow-up that lets
// these four and their *Source interfaces be deleted. It is deliberately not
// bundled with the change that moved the boundary.
//
// It is an Option rather than a positional parameter for the same reason
// WithCredentialBroker is — every other caller (cmd/inroad, cmd/seed, every
// test, and the self-host RoleAll worker) wants the local query and should not
// have to say so — and OFF by default, so an installation that sets nothing
// behaves exactly as it did before this existed.
//
// Passing nil is a no-op, NOT a way to disable suppression checks: silently
// dropping a source that was meant to be wired would leave the process reading
// a pool it was supposed to give up, with nothing saying so.
func WithRemoteSuppression(s SuppressionSource) Option {
	return func(c *client) {
		if s != nil {
			c.suppression = s
		}
	}
}

// WithRemoteJobs replaces the pool-backed per-message job builds with another
// JobSource — in practice internal/coreapi/remote's HTTP client, pointed at the
// control plane's fleet listener. No production caller; see
// WithRemoteSuppression.
//
// This was slice 2 of giving up the worker's pgxpool: eight read methods, end
// to end, over the wire. Same shape and same reasoning as WithRemoteSuppression —
// an Option rather than a positional parameter because every other caller
// (cmd/inroad, cmd/seed, every test, and the self-host RoleAll worker) wants
// the local build and should not have to say so, and OFF by default, so an
// installation that sets nothing behaves exactly as it did before this existed.
//
// Passing nil is a no-op, NOT a way to disable job reads: silently dropping a
// source that was meant to be wired would leave the process reading a pool it
// was supposed to give up, with nothing saying so.
func WithRemoteJobs(s JobSource) Option {
	return func(c *client) {
		if s != nil {
			c.jobs = s
		}
	}
}

// WithRemoteOutcomes replaces the pool-backed claim and outcome WRITES with
// another OutcomeSource — in practice internal/coreapi/remote's HTTP client,
// pointed at the control plane's fleet listener.
//
// No production caller; see WithRemoteSuppression.
//
// This was slice 3 of giving up the worker's pgxpool, and the one that carried
// the risk: over a network a write has a third outcome a function call does not
// — it committed and the response was lost. What makes that safe is the claim,
// not this option; see internal/coreapi/remote's outcomes.go for the per-method
// idempotency audit and the fault-injection tests that prove it.
//
// Same shape and same reasoning as WithRemoteSuppression and WithRemoteJobs — an
// Option rather than a positional parameter because every other caller
// (cmd/inroad, cmd/seed, every test, and the self-host RoleAll worker) wants the
// local write and should not have to say so, and OFF by default, so an
// installation that sets nothing behaves exactly as it did before this existed.
//
// Passing nil is a no-op, NOT a way to disable outcome writes: silently dropping
// a source that was meant to be wired would leave the process writing to a pool
// it was supposed to give up, with nothing saying so.
func WithRemoteOutcomes(s OutcomeSource) Option {
	return func(c *client) {
		if s != nil {
			c.outcomes = s
		}
	}
}

// WithRemoteInboxSends replaces the pool-backed manual reply/compose protocol
// with another InboxSendSource — in practice internal/coreapi/remote's HTTP
// client, pointed at the control plane's fleet listener.
//
// No production caller; see WithRemoteSuppression.
//
// This was slice 3b of giving up the worker's pgxpool, and it carries slice 3's
// risk with a HIGHER bar: these are emails a person wrote and pressed send on, so
// a duplicate is the operator's own words arriving twice in a customer's thread
// rather than one extra marketing touch. What makes a lost response safe is the
// ROW's status-guarded claim, not this option; see
// internal/coreapi/remote/inboxsends.go for the per-method audit of what a lost
// response costs, and the fault-injection tests that drive it.
//
// Same shape and same reasoning as WithRemoteSuppression, WithRemoteJobs and
// WithRemoteOutcomes — an Option rather than a positional parameter because every
// other caller (cmd/inroad, cmd/seed, every test, and the self-host RoleAll
// worker) wants the local path and should not have to say so, and OFF by default,
// so an installation that sets nothing behaves exactly as it did before this
// existed.
//
// Passing nil is a no-op, NOT a way to disable manual sends: silently dropping a
// source that was meant to be wired would leave the process reaching a pool it
// was supposed to give up, with nothing saying so.
func WithRemoteInboxSends(s InboxSendSource) Option {
	return func(c *client) {
		if s != nil {
			c.inboxSends = s
		}
	}
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
// warmup send path). opts carry the optional cross-cutting wiring (see
// WithMetrics); omitting them all yields the same client as before.
//
// A NIL keyring is legal and means "this process cannot open secrets itself":
// every credential path then fails closed with credbroker.ErrNotConfigured
// unless WithCredentialBroker supplies a remote opener. That is the fleet
// worker's configuration — see internal/platform/credbroker.
func New(pool *pgxpool.Pool, keyring *crypto.Keyring, jwtSecret []byte, publicURL string, googleOAuth mail.GoogleOAuth, msOAuth mail.MicrosoftOAuth, warmupSecret []byte, warmupContent warmup.ContentGenerator, opts ...Option) coreapi.Client {
	q := gen.New(pool)
	c := client{
		pool: pool, q: q, jwtSecret: jwtSecret, publicURL: publicURL,
		creds:         NewCredentialOpener(q, keyring, googleOAuth, msOAuth),
		suppression:   localSuppression{q: q},
		enroll:        enrollment.NewService(enrollment.NewPgStore(q)),
		breaker:       deliverability.NewService(deliverability.NewPgStore(pool)),
		warmupSecret:  warmupSecret,
		warmupContent: warmupContent,
		inbox:         newInboxService(pool),
		replyClaims:   idempotency.NewPgStore(pool),
		now:           time.Now,
	}
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// newInboxService builds the EXECUTION plane's view of the inbox domain.
//
// The compose and pending-reply stores must be supplied explicitly. They are
// optional ServiceOptions, and the worker's service was constructed with none
// of them: `inbox.NewService(inbox.NewPgStore(pool))`. That left s.compose and
// s.pending nil, so every claim path failed its ComposeClaimer /
// PendingReplyClaimer type assertion with "this compose store cannot claim" and
// NO composed email or deferred reply could ever be sent. The row stayed
// `scheduled` with an empty last_error while the asynq task retried to
// exhaustion and was archived, so the UI showed a queued message that silently
// never left.
//
// One store satisfies every interface (the split is about who may call what,
// not about where the rows live), so this passes the same *PgStore three times.
// The enqueuers are deliberately NOT wired: the execution plane completes work
// it was handed, it does not schedule new work for itself.
func newInboxService(pool *pgxpool.Pool) *inbox.Service {
	store := inbox.NewPgStore(pool)
	return inbox.NewService(store,
		inbox.WithComposeStore(store),
		inbox.WithPendingReplyStore(store),
	)
}
