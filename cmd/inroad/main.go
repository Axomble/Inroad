package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	// time/tzdata embeds the IANA zone database in the binary. Campaign send
	// windows are evaluated in the campaign's own timezone, and the runtime images
	// are bare alpine with no tzdata package — without this, LoadLocation fails in
	// production for every non-UTC zone. Embedding it also keeps behavior identical
	// across Alpine, a developer's machine, and CI.
	_ "time/tzdata"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/agentchat"
	"github.com/inroad/inroad/internal/app/agentrun"
	"github.com/inroad/inroad/internal/app/agenttool"
	"github.com/inroad/inroad/internal/app/aisettings"
	"github.com/inroad/inroad/internal/app/apikey"
	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/app/contact"
	"github.com/inroad/inroad/internal/app/crm"
	"github.com/inroad/inroad/internal/app/deliverability"
	"github.com/inroad/inroad/internal/app/emailotp"
	"github.com/inroad/inroad/internal/app/idempotency"
	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/app/mailbox"
	"github.com/inroad/inroad/internal/app/mcpserver"
	"github.com/inroad/inroad/internal/app/oauthprovider"
	"github.com/inroad/inroad/internal/app/passkey"
	"github.com/inroad/inroad/internal/app/pulse"
	"github.com/inroad/inroad/internal/app/replylabel"
	"github.com/inroad/inroad/internal/app/reporting"
	"github.com/inroad/inroad/internal/app/sendingdomain"
	"github.com/inroad/inroad/internal/app/sequencestep"
	"github.com/inroad/inroad/internal/app/suppression"
	"github.com/inroad/inroad/internal/app/tracking"
	"github.com/inroad/inroad/internal/app/twofa"
	"github.com/inroad/inroad/internal/app/warmup"
	"github.com/inroad/inroad/internal/platform/ai"
	"github.com/inroad/inroad/internal/platform/captcha"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/dnsauth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/notify"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/ratelimit"
	"github.com/inroad/inroad/internal/platform/throttle"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

// run wires and serves the API. Keeping the body here (rather than in main)
// guarantees the deferred cleanups (stop, pool.Close, enq.Close) run before the
// process exits; main only maps a returned error to a non-zero status code. The
// error paths log/print their own diagnostics before returning.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		// Report before building the logger: config failure could hide log
		// options; matching cmd/migrate keeps bad-config output uniform.
		fmt.Fprintln(os.Stderr, "config:", err)
		return err
	}
	logger := log.New(cfg.Env)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Always constructed (never nil): the API router's metrics middleware
	// records into it unconditionally, and the dedicated /metrics listener
	// below is the only piece that's actually optional (INROAD_METRICS_ADDR).
	//
	// metricsCtx is a CHILD of ctx (not ctx itself): an OS-signal shutdown
	// cancels it automatically alongside the main server below, but run()
	// can also return earlier on a plain error (e.g. db.Connect failing),
	// which never cancels ctx at all — the explicit cancelMetrics() in the
	// deferred cleanup covers that path too, so this can never deadlock
	// waiting on a listener nothing told to stop. The wait itself matters
	// just as much as the cancel: a bare deferred cancel with nothing
	// awaiting the goroutine lets run() (and the process) return before the
	// listener's own graceful srv.Shutdown finishes, which defeats the
	// point of a graceful shutdown.
	mtx := metrics.New()
	metricsCtx, cancelMetrics := context.WithCancel(ctx)
	var metricsWG sync.WaitGroup
	if cfg.MetricsAddr != "" {
		metricsSrv := httpx.NewServer(cfg.MetricsAddr, mtx.Handler())
		metricsWG.Add(1)
		go func() {
			defer metricsWG.Done()
			if err := httpx.Run(metricsCtx, metricsSrv); err != nil {
				logger.Error("metrics server error", "err", err)
			}
		}()
		logger.Info("metrics listening", "addr", cfg.MetricsAddr)
	}
	defer func() {
		cancelMetrics()
		metricsWG.Wait()
	}()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		return err
	}
	defer pool.Close()

	sender, err := notify.New(notify.Config{
		Driver: cfg.TransactionalDriver, SMTPHost: cfg.SystemSMTPHost, SMTPPort: cfg.SystemSMTPPort,
		SMTPUsername: cfg.SystemSMTPUsername, SMTPPassword: cfg.SystemSMTPPassword, From: cfg.SystemEmailFrom,
		AllowPlaintext: cfg.SystemSMTPAllowPlaintext,
		Logger:         logger,
	})
	if err != nil {
		logger.Error("transactional sender init failed", "err", err)
		return err
	}

	queries := gen.New(pool)
	keyring, err := keys.BuildKeyring(cfg, queries)
	if err != nil {
		logger.Error("keyring init failed", "err", err)
		return err
	}
	identStore := identity.NewStore(pool)
	// The store-backed verifier makes access tokens revocable: every request is
	// validated against the session's live revocation/expiry/token_version. It
	// is both the auth.Verifier for the protected group AND the cache-buster the
	// identity handler calls when it revokes a session.
	sessionVerifier := identity.NewSessionVerifier(cfg.JWTSecret, identStore, cfg.SessionCacheTTL)
	identSvc := identity.NewService(identStore, cfg.RefreshTokenTTL, sender, cfg.AppBaseURL,
		cfg.EmailVerifyTTL, cfg.PasswordResetTTL, cfg.InviteTTL,
		// Federated sign-in. A separate Google client from the mailbox-connect one
		// (cfg.Google*), because login requests only openid/email/profile while
		// connect requests restricted Gmail scopes. Unset credentials leave the
		// sign-in endpoints reporting 501 rather than half-wired.
		identity.WithGoogleSignIn(identity.NewGoogleAuthenticator(identity.GoogleSignIn{
			ClientID:     cfg.GoogleSignInClientID,
			ClientSecret: cfg.GoogleSignInClientSecret,
			RedirectURL:  cfg.GoogleSignInRedirectURL,
		}), cfg.JWTSecret))
	// TOTP 2FA. The secret is USER-level, sealed under a server-level HKDF subkey
	// of the master key (crypto.ServerKeyring) — NOT a per-workspace DEK, since a
	// user's second factor spans every workspace they belong to. The twofa service
	// depends on identity only through narrow seams wired below (gate + completer +
	// revoker), never an import.
	serverKeyring, err := crypto.NewServerKeyring(cfg.MasterKey)
	if err != nil {
		logger.Error("server keyring init failed", "err", err)
		return err
	}
	twofaSvc := twofa.NewService(twofa.NewPgStore(pool), serverKeyring)
	// gate = twofaSvc: login interposes the 2FA challenge for confirmed-2FA users.
	identHandler := identity.NewHandler(
		identSvc,
		cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL, cfg.CookieSecure, cfg.CookieDomain,
		cfg.TrustedProxies, sessionVerifier, twofaSvc,
	)
	// completer = identHandler (issues the session on a passed 2FA verify);
	// revoker = identSvc + buster = sessionVerifier (disable revokes other sessions).
	twofaHandler := twofa.NewHandler(twofaSvc, identHandler, identSvc, sessionVerifier)
	// Passkeys / WebAuthn. The Relying Party is derived from INROAD_RP_ID /
	// INROAD_RP_ORIGIN (defaulting from the public URL). If the RP cannot be built
	// (unset/invalid), the service is constructed with a nil library instance so the
	// endpoints fail cleanly (501) — the feature is simply off rather than
	// mis-validating. Like twofa, the passkey domain reaches identity only through
	// the narrow CompleteLogin seam (a user-verified passkey login satisfies MFA and
	// skips the TOTP gate), never an import.
	web, err := passkey.NewWebAuthn(cfg.RPID, cfg.RPOrigin)
	if err != nil {
		logger.Warn("passkeys disabled: relying party not configured", "err", err)
		web = nil
	}
	passkeyHandler := passkey.NewHandler(passkey.NewService(web, passkey.NewPgStore(pool)), identHandler)
	mailboxStore := mailbox.NewPgStore(queries)
	googleOAuth := mail.GoogleOAuth{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.GoogleRedirectURL,
	}
	msOAuth := mail.MicrosoftOAuth{
		ClientID:     cfg.MSClientID,
		ClientSecret: cfg.MSClientSecret,
		RedirectURL:  cfg.MSRedirectURL,
		Tenant:       cfg.MSTenant,
	}
	// Warmup control-plane. Its per-mailbox routes (/mailboxes/{id}/warmup)
	// register as a sub-router under the mailbox mount; its workspace-level
	// overview mounts at /api/v1/warmup below.
	warmupSvc := warmup.NewService(warmup.NewPgStore(queries))
	warmupHandler := warmup.NewHandler(warmupSvc)
	mailboxSvc := mailbox.NewService(mailboxStore, mail.NewNetTester(cfg.MailAllowPrivateHosts), keyring,
		googleOAuth, mailbox.NewGoogleExchanger(googleOAuth),
		msOAuth, mailbox.NewMicrosoftExchanger(msOAuth))
	mbHandler := mailbox.NewHandler(
		mailboxSvc,
		cfg.JWTSecret, cfg.AppBaseURL,
		warmupHandler,
	)

	enq := queue.NewClient(cfg.RedisAddr)
	defer enq.Close()

	// Scoped API keys. The verifier authenticates `inrd_` tokens on the data-plane
	// REST surface (attenuated to the key's granted scopes via RequireScope); the
	// management handler (create/list/revoke) is session-authed and mounts under
	// /api/v1/auth/api-keys. The per-key rate limit uses an atomic Redis window on
	// the same instance the queue runs on, failing closed if Redis is unreachable.
	// The SAME limiter backs the pre-auth login throttles below (keys are namespaced
	// per bucket, so there is no collision).
	redisLimiter := ratelimit.NewRedisLimiter(cfg.RedisAddr)
	defer func() { _ = redisLimiter.Close() }()
	apiKeyStore := apikey.NewPgStore(pool)
	apiKeyHandler := apikey.NewHandler(apikey.NewService(apiKeyStore))
	apiKeyVerifier := apikey.NewVerifier(apiKeyStore, redisLimiter, cfg.TrustedProxies)

	// Pre-authentication hardening: a config-gated captcha (register/login/OTP-start)
	// and per-IP + per-account rate limits on the abusable unauthenticated surface.
	// An empty Turnstile secret selects the no-op verifier (captcha effectively off).
	ipResolver := httpx.NewClientIPResolver(cfg.TrustedProxies)
	var captchaVerifier captcha.Verifier
	if cfg.TurnstileSecret != "" {
		captchaVerifier = captcha.NewTurnstile(cfg.TurnstileSecret, nil)
	} else {
		captchaVerifier = captcha.NewNoop()
	}
	captchaMW := captcha.Middleware(captchaVerifier, ipResolver)
	throttleWindow := time.Minute
	newThrottle := func(bucket string, ipLimit, acctLimit int) func(http.Handler) http.Handler {
		return throttle.Config{
			Limiter: redisLimiter, Resolver: ipResolver, Window: throttleWindow,
			IPLimit: ipLimit, AcctLimit: acctLimit,
		}.Middleware(bucket)
	}
	loginThrottle := newThrottle("login", cfg.RateLimitLoginIP, cfg.RateLimitLoginAccount)
	forgotThrottle := newThrottle("forgot", cfg.RateLimitSensitiveIP, cfg.RateLimitSensitiveAccount)
	otpStartThrottle := newThrottle("otp-start", cfg.RateLimitSensitiveIP, cfg.RateLimitSensitiveAccount)
	otpVerifyThrottle := newThrottle("otp-verify", cfg.RateLimitVerifyIP, cfg.RateLimitVerifyAccount)
	// 2FA/passkey verify carry no email in the body, so they are IP-throttled only.
	twofaVerifyThrottle := newThrottle("2fa-verify", cfg.RateLimitVerifyIP, 0)
	passkeyFinishThrottle := newThrottle("passkey-login", cfg.RateLimitVerifyIP, 0)
	// Dynamic client registration carries no email; IP-throttle only, on the same
	// per-IP cap as the other sensitive unauthenticated endpoints.
	oauthRegisterThrottle := newThrottle("oauth-register", cfg.RateLimitSensitiveIP, 0)
	// Federated sign-in start: IP-only (no account in the request), on the sensitive
	// cap. It bounds an unauthenticated DB write rather than a credential guess --
	// see identity.RouteDeps.GoogleStartThrottle.
	googleStartThrottle := newThrottle("google-signin-start", cfg.RateLimitSensitiveIP, 0)

	// OAuth 2.1 authorization server (Inroad as an OAuth PROVIDER). Self-contained
	// domain mounted under /oauth2 (distinct from the mailbox-connect /oauth mount).
	// The resource owner is resolved from the P1 session through the ResourceOwner
	// seam, backed here by the session verifier (never re-implementing login).
	oauthStore := oauthprovider.NewPgStore(pool)
	oauthProviderHandler := oauthprovider.NewHandler(
		oauthprovider.NewService(oauthStore, cfg.PublicURL),
		sessionResourceOwner{v: sessionVerifier},
	)
	// The OAuth access-token verifier engages on `inoa_` Bearer tokens and DEFERS on
	// everything else, so it slots into the data-plane RequireAuth chain BEFORE the
	// session verifier (which would hard-fail a non-JWT Bearer token). It mints a
	// scope-only KindOAuth principal, attenuated by each route's RequireScope.
	oauthVerifier := oauthprovider.NewVerifier(oauthStore)

	// Email-OTP passwordless login. A dedicated slice (its own store/service) that
	// reaches session-issuance + the 2FA gate only through identHandler's
	// CompleteFirstFactor seam, so an OTP login runs the SAME first-factor path (and
	// the SAME 2FA gate) a password login does — never a session from email alone.
	emailOTPHandler := emailotp.NewHandler(emailotp.NewService(emailotp.NewPgStore(pool), sender), identHandler)
	listSvc := list.NewService(list.NewPgStore(queries))
	// contact takes only a small ListChecker interface (not the whole list
	// service) so the contact package doesn't have to import app/list —
	// keeps the "app packages don't import each other" invariant intact.
	contactStore := contact.NewPgStore(pool)
	// Custom field definitions are a second narrow seam on the same domain
	// (workspace-defined typed fields whose values live in contacts.custom_fields).
	contactFieldStore := contact.NewPgFieldStore(queries)
	contactSvc := contact.NewService(contactStore, listCheckerAdapter{lists: listSvc}, contactFieldStore)
	contactHandler := contact.NewHandler(contactSvc)
	// Sending-domain authentication (SPF/DKIM/DMARC). Built here (not inline at
	// its mount below) because campaign preflight's domain_auth check also
	// reads it, through the narrow domainAuthAdapter — the domain list is
	// derived from this workspace's mailboxes, so nothing here reaches DNS.
	sendingdomainSvc := sendingdomain.NewService(sendingdomain.NewPgStore(queries), dnsauth.NewResolver())
	// checker adapts the mailbox and list stores for campaign ownership checks.
	campaignStore := campaign.NewPgStore(pool)
	// Built here (not inline at its mount below) because campaign test-send's
	// suppression check also reads it, through campaign.SuppressionChecker --
	// suppStore satisfies that interface structurally, so no adapter type is
	// needed (unlike domainAuthAdapter, whose source returns a different shape).
	suppStore := suppression.NewStore(queries)
	campaignSvc := campaign.NewService(campaignStore, ownershipChecker{mailboxes: mailboxStore, lists: listSvc},
		campaign.WithDomainAuth(domainAuthAdapter{domains: sendingdomainSvc}),
		// The personalization_tokens preflight check needs to know which
		// {{custom.*}} keys the workspace actually defines; contact owns them.
		campaign.WithCustomFields(customFieldAdapter{contacts: contactSvc}),
		// Per-step / per-variant reporting aggregates.
		campaign.WithResults(campaign.NewPgResultsStore(queries)),
		// Test-send (POST /campaigns/{id}/test-send) only ENQUEUES a
		// testsend:send task here: cmd/inroad must never decrypt a mailbox
		// credential or dial a provider (docs/security.md invariant 1). The
		// actual render+send happens in the execution plane
		// (internal/worker/testsend), which resolves the mailbox transport
		// through the same coreapi credential path every real send uses.
		campaign.WithTestSendEnqueuer(enq),
		// Reuses the same Redis-backed limiter the auth throttles use, so the
		// 5/min test-send cap holds across every API server instance.
		campaign.WithRateLimiter(redisLimiter),
		// A test-send must never reach an address the workspace has explicitly
		// unsubscribed or bounced -- the SAME suppression table a real send
		// checks. internal/worker/testsend re-checks independently before
		// dialing (defense in depth against a race with an incoming
		// unsubscribe between enqueue and the task running).
		campaign.WithSuppressionChecker(suppStore),
	)
	// Sequence steps live under /campaigns/{id}/steps; the step service checks
	// campaign status (draft-gating) via an adapter over the campaign store.
	stepHandler := sequencestep.NewHandler(
		sequencestep.NewService(sequencestep.NewPgStore(pool), campaignStatusChecker{campaigns: campaignStore},
			sequencestep.NewPgVariantStore(queries)),
		cfg.JWTSecret,
	)
	// Deliverability guardrails. One service backs BOTH the API endpoints and the
	// worker's circuit breaker (through coreapi), so the number an operator reads
	// and the verdict that stops a campaign come from one computation.
	deliverabilitySvc := deliverability.NewService(deliverability.NewPgStore(pool))
	deliverabilityHandler := deliverability.NewHandler(deliverabilitySvc)
	pulseSvc := pulse.NewService(pulse.NewPgStore(queries))
	// Cross-campaign performance. Its own domain rather than a campaign
	// endpoint: it answers a workspace-level question (which campaign is
	// working) from one query across every campaign, where campaign.Service
	// deliberately computes and caches one campaign at a time.
	reportingSvc := reporting.NewService(reporting.NewPgStore(queries))
	// The reply-label taxonomy: which buckets a reply is classified into and,
	// through each label's role flags, what automation that triggers. The
	// execution plane reads the same table through coreapi, so the operator's
	// edit and the poller's dispatch cannot drift.
	replyLabelSvc := replylabel.NewService(replylabel.NewPgStore(pool))
	// Auto-capture is no longer pinned to reply_class="positive": it fires for
	// any label carrying captures_deal, read through the narrow
	// replyLabelAdapter (app/* packages never import each other). Unwired it
	// degrades to the old literal rule, so a deployment mid-migration keeps
	// capturing positives.
	crmSvc := crm.NewService(crm.NewPgStore(pool), crm.WithReplyLabels(replyLabelAdapter{labels: replyLabelSvc}))
	crmHandler := crm.NewHandler(crmSvc)
	// AI settings (agent platform PR A1). No shipped model catalog: native
	// model metadata comes from models.dev at runtime, cached in Postgres with
	// serve-stale-on-failure. Provider credentials seal under the same
	// per-workspace DEK keyring as mailbox credentials; user-supplied
	// base_url/endpoint hosts vet through the mail package's SSRF classifier
	// at write time AND through the guarded transport at discovery-dial time.
	catalogSource := ai.NewCatalogSource(ai.NewPgCatalogCache(queries), "")
	aiHandler := aisettings.NewHandler(aisettings.NewService(aisettings.ServiceDeps{
		Store:               aisettings.NewPgStore(queries),
		Keyring:             keyring,
		Catalog:             catalogSource,
		Discoverer:          ai.NewHTTPDiscoverer(cfg.AIAllowPrivateBaseURL, 0),
		ClassifyHost:        mail.ClassifyHost,
		AllowPrivateBaseURL: cfg.AIAllowPrivateBaseURL,
	}))
	agentStore := agentchat.NewPgStore(pool)
	if recovered, err := agentStore.RecoverStuckRuns(ctx, "API restarted while the agent was running"); err != nil {
		logger.Error("agent stuck-run recovery failed", "err", err)
		return err
	} else if recovered > 0 {
		logger.Warn("recovered interrupted agent runs", "count", recovered)
	}
	agentStream := agentchat.NewRedisStream(cfg.RedisAddr)
	defer func() { _ = agentStream.Close() }()
	go func() {
		if err := agentStream.ListenCancellations(ctx); err != nil && ctx.Err() == nil {
			logger.Error("agent cancellation listener stopped", "err", err)
		}
	}()
	toolRegistry := agenttool.New(agenttool.Deps{
		Campaigns:       campaignSvc,
		Contacts:        contactTools{service: contactSvc, store: contactStore, lists: listSvc, pool: pool},
		ContactWrites:   contactTools{service: contactSvc, store: contactStore, lists: listSvc, pool: pool},
		ContactImports:  contactTools{service: contactSvc, store: contactStore, lists: listSvc, pool: pool},
		Mailboxes:       mailboxTools{service: mailboxSvc},
		Deliverability:  deliverabilityToolAdapter{deliverability: deliverabilitySvc, pulse: pulseSvc},
		Lists:           listSvc,
		ListWrites:      listSvc,
		Warmup:          warmupTools{service: warmupSvc},
		CRM:             crmTools{service: crmSvc},
		CRMErrors:       crmErrors{},
		CRMWriteLimiter: redisLimiter,
	})
	mcpResourceURL := strings.TrimRight(cfg.PublicURL, "/") + "/v1/mcp"
	mcpHandler := mcpserver.New(toolRegistry, func(ctx context.Context, r *http.Request) (agenttool.Principal, []string, time.Time, string, bool, error) {
		p, expiresAt, clientID, ok, err := oauthVerifier.VerifyToken(ctx, r)
		if err != nil || !ok {
			return agenttool.Principal{}, nil, time.Time{}, "", false, err
		}
		userID, err := uuid.Parse(p.UserID)
		if err != nil {
			return agenttool.Principal{}, nil, time.Time{}, "", false, err
		}
		workspaceID, err := uuid.Parse(p.WorkspaceID)
		if err != nil {
			return agenttool.Principal{}, nil, time.Time{}, "", false, err
		}
		return agenttool.Principal{WorkspaceID: workspaceID, UserID: userID, Role: "member", AgentClientID: clientID}, p.Scopes, expiresAt, clientID, true, nil
	}, mcpResourceURL, strings.TrimRight(cfg.PublicURL, "/")+"/oauth2")
	modelResolver := agentchat.NewPgModelResolver(
		queries, keyring, catalogSource, ai.NewStreamerFactory(cfg.AIAllowPrivateBaseURL),
	)
	runtime := &agentrun.Runtime{
		Store: agentStore, Models: modelResolver, Tools: runtimeTools{registry: toolRegistry},
		Publisher: agentStream, Approvals: agentStore, Logger: logger,
	}
	runManager := agentrun.NewManager(ctx, runtime, agentStore, agentStream, agentStream, logger, cfg.AgentMaxConcurrentRuns)
	runManager.StartExpirySweep()
	agentHandler := agentchat.NewHandler(agentchat.NewService(agentStore, runManager, agentStream))

	// Unified inbox (thread/message read model). The write path (RecordReply)
	// is called by the reply-polling worker through its own coreapi seam
	// (internal/coreapi/inprocess), not from here; this handler is the
	// read/mark-read HTTP surface, PLUS POST /threads/{id}/reply, which only
	// enqueues (internal/worker/inbox does the actual decrypt+dial —
	// docs/security.md invariant 1), and POST /threads/{id}/draft-reply, which
	// generates suggested reply text and sends nothing at all (invariant 48).
	//
	// Built here, after the agent runtime, because the draft path reuses THAT
	// runtime's model resolver rather than constructing a second one — one
	// workspace AI configuration, one credential-unsealing path.
	// One PgStore, handed in three times: it implements inbox.Store (threads and
	// messages), inbox.SnoozeStore and inbox.LabelStore (triage state). The
	// interfaces are separate so a caller needing only one need not satisfy the
	// others, not because the persistence is.
	inboxStore := inbox.NewPgStore(pool)
	inboxHandler := inbox.NewHandler(inbox.NewService(inboxStore,
		// A manual reply must never go to an address the workspace has
		// explicitly unsubscribed or bounced — the SAME suppression table a
		// real send checks. internal/worker/inbox re-checks independently
		// before dialing (defense in depth against a race with an incoming
		// unsubscribe between enqueue and the task running).
		inbox.WithSuppressionChecker(suppStore),
		inbox.WithReplyEnqueuer(enq),
		inbox.WithReplyDrafter(replyDrafterAdapter{runtime: runtime}),
		inbox.WithSnoozeStore(inboxStore),
		inbox.WithLabelStore(inboxStore),
	))
	// Per-IP and per-WORKSPACE cap on reply drafting. Unlike the pre-auth
	// throttles above, the account key comes from the authenticated principal
	// rather than a body "email": this endpoint's body is empty, and the budget a
	// draft spends belongs to the workspace, not to whichever member clicked.
	draftReplyThrottle := throttle.Config{
		Limiter: redisLimiter, Resolver: ipResolver, Window: throttleWindow,
		IPLimit: cfg.RateLimitDraftReplyIP, AcctLimit: cfg.RateLimitDraftReplyWorkspace,
		AcctKey: func(r *http.Request) string {
			p, ok := auth.UserFromContext(r.Context())
			if !ok {
				return "" // unreachable behind RequireAuth; IP-throttle only if it happens
			}
			return p.WorkspaceID
		},
	}.Middleware("inbox-draft-reply")

	trackHandler := tracking.NewHandler(tracking.NewService(cfg.TrackingSecret, tracking.NewPgStore(pool)))

	// Deny-by-default routing. Two groups:
	//   public    - reachable without an access token. Either genuinely open
	//               (/healthz from NewRouter, the /u unsubscribe link) or
	//               self-guarding: the identity handler interleaves public
	//               register/login, CSRF-gated refresh/logout, and
	//               token-protected me/logout-all/switch-workspace under one
	//               /api/v1/auth prefix, so it applies auth internally rather
	//               than via the blanket group (chi can't Mount two subrouters
	//               on the same prefix, and refresh/logout must work once the
	//               access token has expired).
	//   protected - wrapped ONCE by auth.RequireAuth at the group root. Any
	//               route mounted here is authenticated by default, so a new
	//               domain that forgets its own middleware still fails closed.
	public := []mount{
		{pattern: "/api/v1/auth", handler: identHandler.Routes(identity.RouteDeps{
			Verifier: sessionVerifier,
			TwoFA:    twofaHandler.Routes(sessionVerifier, twofaVerifyThrottle),
			Passkeys: passkeyHandler.Routes(sessionVerifier, passkeyFinishThrottle),
			APIKeys:  apiKeyHandler.Routes(sessionVerifier),
			// otpStartThrottle is OUTERMOST (listed first) so the cheap local Redis
			// rate-limit sheds an over-cap /email-otp/start with a 429 BEFORE captcha
			// fires its outbound siteverify round-trip. Throttle must gate captcha.
			EmailOTP:            emailOTPHandler.Routes([]func(http.Handler) http.Handler{otpStartThrottle, captchaMW}, []func(http.Handler) http.Handler{otpVerifyThrottle}),
			Captcha:             captchaMW,
			LoginThrottle:       loginThrottle,
			ForgotThrottle:      forgotThrottle,
			GoogleStartThrottle: googleStartThrottle,
		})},
		{pattern: "/u", handler: suppression.NewHandler(cfg.JWTSecret, suppStore).Routes()},
		// Recipients follow open-pixel/click-redirect links unauthenticated,
		// same as /u — mounted here, not the protected group.
		{pattern: "/t", handler: trackHandler.Routes()},
		// OAuth callbacks (Gmail, M365) are top-level browser navigations from
		// the provider; they authenticate from the signed state, not the JWT
		// cookie, so they mount here rather than the protected group.
		{pattern: "/oauth", handler: mbHandler.CallbackRoutes()},
		// OAuth 2.1 authorization server (provider). Mounted at /oauth2 — distinct
		// from the /oauth mailbox-connect mount above. /authorize is a top-level
		// browser navigation that resolves the resource owner from the session seam
		// (unauth -> login redirect), so it is public here rather than in a
		// RequireAuth group; its sub-routes apply their own session/admin/CSRF guards.
		{pattern: "/oauth2", handler: oauthProviderHandler.Routes(sessionVerifier, oauthRegisterThrottle)},
		{pattern: "/oauth2/.well-known/oauth-authorization-server", handler: oauthprovider.AuthorizationServerMetadata(cfg.PublicURL)},
		// MCP clients discover the protected-resource metadata without a bearer
		// token; the stream endpoint performs its own OAuth bearer validation.
		{pattern: "/.well-known/oauth-protected-resource", handler: mcpHandler.Metadata()},
		{pattern: "/v1/mcp", handler: mcpHandler.StreamableHTTP()},
	}
	// The data-plane REST surface accepts a session, an api-key, OR an OAuth access
	// token. The api-key verifier runs first and DEFERS on a non-`inrd_` token; the
	// OAuth verifier then engages on an `inoa_` token and DEFERS otherwise; a JWT falls
	// through to the session verifier. A machine principal (api-key or OAuth grant) is
	// attenuated to its granted scopes by each route's RequireScope (a session holds all
	// scopes). Ordering matters: the OAuth verifier MUST precede the session verifier,
	// which hard-fails a non-JWT Bearer token rather than deferring.
	dataPlane := []mount{
		{pattern: "/api/v1/mailboxes", handler: mbHandler.Routes(identStore)},
		{pattern: "/api/v1/lists", handler: list.NewHandler(listSvc).Routes()},
		// Mounted at /api/v1/contacts (not /api/v1) to avoid the chi mount-prefix
		// overlap with /api/v1/lists that would otherwise 404 the import route.
		// Surface: POST /api/v1/contacts/import?list={id}, GET /api/v1/contacts?list={id}.
		{pattern: "/api/v1/contacts", handler: contactHandler.Routes()},
		// Custom field DEFINITIONS are a workspace setting rather than a
		// sub-resource of any one contact, so they get their own mount instead
		// of sitting at /contacts/fields, ambiguously beside /contacts/{id}.
		{pattern: "/api/v1/custom-fields", handler: contactHandler.FieldRoutes()},
		{pattern: "/api/v1/crm", handler: crmHandler.Routes()},
		// Reply-label taxonomy CRUD + reorder. Gated on the campaign scopes
		// inside Routes(): a label's role flags are send-automation config.
		{pattern: "/api/v1/reply-labels", handler: replylabel.NewHandler(replyLabelSvc).Routes()},
		// Unified inbox: GET /threads, GET /threads/{id}, PUT /threads/{id}/read.
		// Scope-gated per route inside Routes() (inbox:read / inbox:write); a
		// session principal holds both implicitly.
		{pattern: "/api/v1/inbox", handler: inboxHandler.Routes(draftReplyThrottle)},
		// Sequence steps register as a SubRouter under the campaigns mount, so
		// /campaigns/{id}/steps lives under this group and inherits its RequireAuth.
		// Routes(identStore) additionally applies RequireVerified to /launch
		// (email-gated sending).
		// The deliverability sub-router registers /campaigns/{id}/deliverability and
		// /campaigns/{id}/guardrails here rather than on its own mount, so they live
		// under the campaigns prefix (chi cannot mount two routers on one prefix).
		{pattern: "/api/v1/campaigns", handler: campaign.NewHandler(campaignSvc, enq, stepHandler, deliverabilityHandler).Routes(identStore)},
		// Cross-campaign performance rollup. In the data plane, not session-only:
		// an external dashboard pulling this with campaigns:read is a legitimate
		// use, and it exposes nothing that scope couldn't already assemble one
		// campaign at a time.
		{pattern: "/api/v1/reports", handler: reporting.NewHandler(reportingSvc).Routes()},
		// Sending-domain authentication (SPF/DKIM/DMARC). Read-only status plus an
		// on-demand recheck; the domain list is derived from this workspace's
		// mailboxes, and the recheck resolves a domain ONLY after confirming the
		// workspace sends from it (404 otherwise), so it is not a resolver proxy.
		// Deliverability rollup + the MACHINE event-ingest endpoint. Mounted on the
		// data plane because ingest is api-key authenticated: an external bounce/
		// complaint pipeline is exactly the caller this group exists for.
		{pattern: "/api/v1/deliverability", handler: deliverabilityHandler.Routes()},
		{pattern: "/api/v1/sending-domains", handler: sendingdomain.NewHandler(sendingdomainSvc).Routes()},
	}
	// Session-only surface: workspace administration and the warmup overview are not
	// part of the api-key contract, so they authenticate with the session verifier
	// alone — an `inrd_` token presented here is rejected 401 (fail closed).
	sessionOnly := []mount{
		{pattern: "/api/v1/workspaces", handler: identHandler.WorkspaceRoutes()},
		// Workspace-level warmup overview. The per-mailbox warmup routes
		// (/mailboxes/{id}/warmup) are registered as a sub-router of the mailbox
		// mount above, not here.
		{pattern: "/api/v1/warmup", handler: warmupHandler.Routes()},
		// The console's aggregate read-model (pulse card / nav counts /
		// overview tiles). Read-only, workspace-pinned, chrome-only — not part
		// of the api-key contract.
		{pattern: "/api/v1/pulse", handler: pulse.NewHandler(pulseSvc).Routes()},
		// Workspace AI configuration (model defaults, sealed provider keys).
		// Session-only: writes are further gated to admins/owners inside
		// Routes(), and provider keys are never part of the api-key contract.
		{pattern: "/api/v1/ai", handler: aiHandler.Routes()},
		// The in-app agent always acts on behalf of a human session. API keys and
		// OAuth clients cannot create threads or inherit a user's tool authority.
		{pattern: "/api/v1/agent", handler: agentHandler.Routes()},
	}
	// Idempotency-Key replay cache: generic cross-cutting middleware, mounted
	// inside every authenticated group (after RequireAuth resolves the
	// principal) and before the domain routers. The workspace-id accessor is a
	// small function seam rather than an import of app/auth — platform/*
	// must never import app/* — using the non-writing UserFromContext (this
	// middleware only ever runs inside an already-authenticated group, so a
	// missing principal here is a programming error the middleware itself
	// fails closed on, rather than the request-writing auth.WorkspaceID).
	idempotencyStore := idempotency.NewPgStore(pool)
	idempotencyMW := httpx.Idempotency(idempotencyStore, func(r *http.Request) (string, bool) {
		p, ok := auth.UserFromContext(r.Context())
		if !ok {
			return "", false
		}
		return p.WorkspaceID, true
	}, skipIdempotencyGuard)

	router := buildRouter(logger, mtx, public, []protectedGroup{
		{verifiers: []auth.Verifier{apiKeyVerifier, oauthVerifier, sessionVerifier}, mounts: dataPlane},
		{verifiers: []auth.Verifier{sessionVerifier}, mounts: sessionOnly},
	}, idempotencyMW)
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			httpx.Error(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	if spa, ok := httpx.SPA(cfg.WebDir); ok {
		router.NotFound(spa.ServeHTTP)
		logger.Info("serving web app", "dir", cfg.WebDir)
	} else if cfg.WebDir != "" {
		logger.Warn("web app directory unavailable; API-only mode", "dir", cfg.WebDir)
	}

	srv := httpx.NewServer(cfg.HTTPAddr, router)
	logger.Info("api listening", "addr", cfg.HTTPAddr)
	if err := httpx.Run(ctx, srv); err != nil {
		logger.Error("server error", "err", err)
		return err
	}
	return nil
}

// mount pairs a URL prefix with the handler served under it.
type mount struct {
	pattern string
	handler http.Handler
}

// protectedGroup is a set of mounts sharing one authentication chain: the group
// root wraps them once in auth.RequireAuth(verifiers...). Different groups can
// accept different credentials (e.g. the data plane adds the api-key verifier
// ahead of the session verifier).
type protectedGroup struct {
	verifiers []auth.Verifier
	mounts    []mount
}

// buildRouter assembles the API router with a deny-by-default posture. Public
// mounts are served as-is; every protected group is wrapped once, at its group
// root, by auth.RequireAuth(verifiers...). A route added under a protected group
// is therefore authenticated whether or not it wires up any middleware of its
// own -- forgetting a per-domain guard can no longer expose a route.
//
// groupMiddleware applies to every protected group, AFTER RequireAuth (so it
// can read the authenticated principal) and BEFORE the group's own domain
// routers mount -- currently just the Idempotency-Key replay cache, which
// needs an authenticated workspace and must run ahead of every handler it
// might short-circuit a replay for.
func buildRouter(logger *slog.Logger, mtx *metrics.Metrics, public []mount, groups []protectedGroup, groupMiddleware ...func(http.Handler) http.Handler) *chi.Mux {
	r := httpx.NewRouter(logger, mtx)
	for _, m := range public {
		r.Mount(m.pattern, m.handler)
	}
	for _, g := range groups {
		r.Group(func(pr chi.Router) {
			pr.Use(auth.RequireAuth(g.verifiers...))
			for _, mw := range groupMiddleware {
				pr.Use(mw)
			}
			for _, m := range g.mounts {
				pr.Mount(m.pattern, m.handler)
			}
		})
	}
	return r
}

// ownershipChecker adapts the mailbox and list stores to campaign.Checker so
// campaign creation/launch can verify cross-domain references belong to the
// caller's workspace without the campaign package importing those domains.
type ownershipChecker struct {
	mailboxes mailbox.Store
	lists     *list.Service
}

// MailboxActive reports whether mailboxID exists in the workspace and is
// active. A missing mailbox (pgx.ErrNoRows) is (false, nil) — a legitimate
// "not yours or gone" answer that shouldn't 500 the caller. Any other
// error surfaces so callers see genuine DB failures instead of silent
// misses.
func (o ownershipChecker) MailboxActive(ctx context.Context, ws, mailboxID uuid.UUID) (bool, error) {
	m, err := o.mailboxes.Get(ctx, ws, mailboxID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return m.Status == "active", nil
}

// ListExists reports whether listID exists in the workspace. Same treatment
// as MailboxActive: no-rows is not an error, anything else is.
func (o ownershipChecker) ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error) {
	_, err := o.lists.Get(ctx, ws, listID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// domainAuthAdapter satisfies campaign.DomainAuthReader over the sendingdomain
// service, so the preflight domain_auth check reads real SPF/DMARC verdicts
// without the campaign package importing sendingdomain (app/* packages never
// import each other).
type domainAuthAdapter struct{ domains *sendingdomain.Service }

func (a domainAuthAdapter) DomainAuth(ctx context.Context, ws uuid.UUID) (map[string]campaign.DomainAuthVerdict, error) {
	rows, err := a.domains.List(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make(map[string]campaign.DomainAuthVerdict, len(rows))
	for _, d := range rows {
		out[d.Domain] = campaign.DomainAuthVerdict{
			Checked: d.CheckedAt != nil, SPFFound: d.SPFFound, DMARCFound: d.DMARCFound,
		}
	}
	return out, nil
}

// replyLabelAdapter satisfies crm.ReplyLabelReader over the replylabel service,
// so CRM auto-capture can ask "does this label capture a deal?" without the crm
// package importing replylabel (app/* packages never import each other) — the
// same shape as domainAuthAdapter above.
type replyLabelAdapter struct{ labels *replylabel.Service }

func (a replyLabelAdapter) CapturesDeal(ctx context.Context, ws uuid.UUID, key string) (bool, bool, error) {
	label, ok, err := a.labels.Resolve(ctx, ws, key)
	if err != nil || !ok {
		return false, false, err
	}
	return label.CapturesDeal, true, nil
}

// draftReplyPath matches the ONE route the Idempotency-Key guard skips:
// POST /api/v1/inbox/threads/{id}/draft-reply.
//
// Anchored at both ends with the id constrained to a single segment ([^/]+),
// deliberately NOT a suffix match: a suffix would hand the bypass to any future
// ".../draft-reply" route in any other domain, and a replay guard that is
// silently skipped is not something anyone notices until it matters.
//
// A regex rather than a chi route pattern because this predicate runs at the
// authenticated-group root, BEFORE any domain router has resolved a pattern. It
// therefore matches the full path INCLUDING the /api/v1/inbox mount prefix,
// which is what the guard sees: it is installed with pr.Use ahead of the mounts,
// and chi's Mount does not rewrite r.URL.Path.
//
// Anchoring on that prefix fails in the SAFE direction if the mount ever moves —
// the pattern stops matching, the guard re-engages, and idempotency is enforced
// rather than silently dropped.
//
// Compiled once at init, since this runs on every authenticated request.
var draftReplyPath = regexp.MustCompile(`^/api/v1/inbox/threads/[^/]+/draft-reply$`)

// skipIdempotencyGuard opts POST .../draft-reply out of the Idempotency-Key
// replay cache (httpx.SkipFunc). Two independent reasons, either sufficient:
//
// Replaying is the wrong answer. A draft is a generation, not a state change:
// a caller repeating the request wants a NEW draft, and there is no duplicate
// side effect for the cache to prevent — the endpoint writes nothing and sends
// no mail. Cost is bounded by the endpoint's own per-IP/per-workspace rate
// limit, which is the right instrument for spend.
//
// It would also make an error misleading. The guard answers a key reused across
// a DIFFERENT request with 422 idempotency_key_reuse, and this route already
// uses 422 for "no AI model is configured for this workspace" — the one status
// its client is told it can branch on without reading the body. Two meanings
// behind one status would have the UI offer "configure a model in Settings → AI"
// for what is actually a client bug.
func skipIdempotencyGuard(r *http.Request) bool {
	return draftReplyPath.MatchString(r.URL.Path)
}

// replyDrafterAdapter satisfies inbox.ReplyDrafter over the agent runtime's
// one-shot draft call, so the inbox domain can ask for suggested reply text
// without importing agentrun/agentchat (app/* packages never import each other).
// It only translates shapes: the runtime owns the prompt, the caps, the timeout
// and the output normalization, and the inbox domain owns the thread it is
// allowed to read. Nothing here can send mail.
type replyDrafterAdapter struct{ runtime *agentrun.Runtime }

func (a replyDrafterAdapter) DraftReply(ctx context.Context, ws uuid.UUID, in inbox.DraftReplyInput) (string, error) {
	turns := make([]agentrun.DraftTurn, 0, len(in.Turns))
	for _, t := range in.Turns {
		turns = append(turns, agentrun.DraftTurn{FromContact: t.FromContact, Text: t.Text})
	}
	return a.runtime.DraftReply(ctx, ws, agentrun.DraftReplyInput{
		ContactFirstName: in.ContactFirstName,
		Subject:          in.Subject,
		FromCampaign:     in.FromCampaign,
		Turns:            turns,
	})
}

// campaignStatusChecker adapts the campaign store to sequencestep.CampaignChecker
// so the steps domain can draft-gate structural edits without importing the
// campaign package. A missing campaign surfaces as an error, which the step
// service maps to "campaign not found" (404).
type campaignStatusChecker struct{ campaigns *campaign.PgStore }

func (c campaignStatusChecker) CampaignStatus(ctx context.Context, ws, campaignID uuid.UUID) (string, error) {
	cam, err := c.campaigns.Get(ctx, ws, campaignID)
	if err != nil {
		return "", err
	}
	return cam.Status, nil
}

// sessionResourceOwner backs the oauthprovider.ResourceOwner seam with the session
// verifier: it resolves the current resource owner from the P1 session on the request
// WITHOUT the oauthprovider domain importing identity (dependency inversion — the
// domain defines the interface, the composition root supplies this identity-backed
// impl). A definitive "not authenticated" (deferred verifier, or ErrUnauthorized) is
// reported as (false, nil) so /authorize sends the user to log in rather than 500ing;
// only a genuine infra failure surfaces as an error. A non-session (machine)
// principal is treated as "no resource owner" — an API key cannot grant OAuth consent.
type sessionResourceOwner struct{ v auth.Verifier }

func (s sessionResourceOwner) Resolve(ctx context.Context, r *http.Request) (oauthprovider.Owner, bool, error) {
	p, ok, err := s.v.Verify(ctx, r)
	if err != nil {
		if errors.Is(err, auth.ErrUnauthorized) {
			return oauthprovider.Owner{}, false, nil
		}
		return oauthprovider.Owner{}, false, err
	}
	if !ok || p.Kind != auth.KindSession {
		return oauthprovider.Owner{}, false, nil
	}
	uid, wid, parsed := parseOwnerIDs(p.UserID, p.WorkspaceID)
	if !parsed {
		// A malformed principal is treated as "no resource owner", not an infra
		// error, so /authorize sends the user to log in rather than 500ing.
		return oauthprovider.Owner{}, false, nil
	}
	return oauthprovider.Owner{UserID: uid, WorkspaceID: wid}, true, nil
}

// parseOwnerIDs parses a principal's user + workspace ids, reporting ok=false if
// either is malformed. Returning a bool (not an error) keeps the caller's
// "not authenticated" path free of a lingering error value.
func parseOwnerIDs(userID, workspaceID string) (uuid.UUID, uuid.UUID, bool) {
	uid, err := uuid.Parse(userID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	wid, err := uuid.Parse(workspaceID)
	if err != nil {
		return uuid.Nil, uuid.Nil, false
	}
	return uid, wid, true
}

// customFieldAdapter satisfies campaign.CustomFieldReader over contact.Service,
// so the campaign package can validate {{custom.*}} tokens without importing
// app/contact. Same shape as domainAuthAdapter.
//
// Only LIVE keys are returned: an archived field still has values on existing
// contacts, but nothing new can be given one, so a token naming it renders
// blank for every contact imported since it was retired — which is exactly the
// silent failure the check exists to catch.
type customFieldAdapter struct{ contacts *contact.Service }

func (a customFieldAdapter) CustomFieldKeys(ctx context.Context, ws uuid.UUID) ([]string, error) {
	defs, err := a.contacts.ListFieldDefs(ctx, ws)
	if err != nil {
		return nil, err
	}
	liveKeys := make([]string, 0, len(defs))
	for _, d := range defs {
		if d.Live() {
			liveKeys = append(liveKeys, d.Key)
		}
	}
	return liveKeys, nil
}

// listCheckerAdapter satisfies contact.ListChecker so the contact package
// doesn't have to import app/list directly. Same distinction as
// ownershipChecker: pgx.ErrNoRows → (false, nil); real DB errors surface.
type listCheckerAdapter struct{ lists *list.Service }

func (a listCheckerAdapter) ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error) {
	_, err := a.lists.Get(ctx, ws, listID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
