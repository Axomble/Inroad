package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/apikey"
	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/app/contact"
	"github.com/inroad/inroad/internal/app/emailotp"
	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/app/mailbox"
	"github.com/inroad/inroad/internal/app/passkey"
	"github.com/inroad/inroad/internal/app/sequencestep"
	"github.com/inroad/inroad/internal/app/suppression"
	"github.com/inroad/inroad/internal/app/tracking"
	"github.com/inroad/inroad/internal/app/twofa"
	"github.com/inroad/inroad/internal/app/warmup"
	"github.com/inroad/inroad/internal/platform/captcha"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/mail"
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

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		return err
	}
	defer pool.Close()

	sender, err := notify.New(notify.Config{
		Driver: cfg.TransactionalDriver, SMTPHost: cfg.SystemSMTPHost, SMTPPort: cfg.SystemSMTPPort,
		SMTPUsername: cfg.SystemSMTPUsername, SMTPPassword: cfg.SystemSMTPPassword, From: cfg.SystemEmailFrom,
		Logger: logger,
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
		cfg.EmailVerifyTTL, cfg.PasswordResetTTL, cfg.InviteTTL)
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
	warmupHandler := warmup.NewHandler(warmup.NewService(warmup.NewPgStore(queries)))
	mbHandler := mailbox.NewHandler(
		mailbox.NewService(mailboxStore, mail.NewNetTester(cfg.MailAllowPrivateHosts), keyring,
			googleOAuth, mailbox.NewGoogleExchanger(googleOAuth),
			msOAuth, mailbox.NewMicrosoftExchanger(msOAuth)),
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

	// Email-OTP passwordless login. A dedicated slice (its own store/service) that
	// reaches session-issuance + the 2FA gate only through identHandler's
	// CompleteFirstFactor seam, so an OTP login runs the SAME first-factor path (and
	// the SAME 2FA gate) a password login does — never a session from email alone.
	emailOTPHandler := emailotp.NewHandler(emailotp.NewService(emailotp.NewPgStore(pool), sender), identHandler)
	listSvc := list.NewService(list.NewPgStore(queries))
	// contact takes only a small ListChecker interface (not the whole list
	// service) so the contact package doesn't have to import app/list —
	// keeps the "app packages don't import each other" invariant intact.
	contactSvc := contact.NewService(contact.NewPgStore(queries), listCheckerAdapter{lists: listSvc})
	// checker adapts the mailbox and list stores for campaign ownership checks.
	campaignStore := campaign.NewPgStore(pool)
	campaignSvc := campaign.NewService(campaignStore, ownershipChecker{mailboxes: mailboxStore, lists: listSvc})
	// Sequence steps live under /campaigns/{id}/steps; the step service checks
	// campaign status (draft-gating) via an adapter over the campaign store.
	stepHandler := sequencestep.NewHandler(
		sequencestep.NewService(sequencestep.NewPgStore(pool), campaignStatusChecker{campaigns: campaignStore}),
		cfg.JWTSecret,
	)
	suppStore := suppression.NewStore(queries)
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
			Verifier:       sessionVerifier,
			TwoFA:          twofaHandler.Routes(sessionVerifier, twofaVerifyThrottle),
			Passkeys:       passkeyHandler.Routes(sessionVerifier, passkeyFinishThrottle),
			APIKeys:        apiKeyHandler.Routes(sessionVerifier),
			EmailOTP:       emailOTPHandler.Routes([]func(http.Handler) http.Handler{captchaMW, otpStartThrottle}, []func(http.Handler) http.Handler{otpVerifyThrottle}),
			Captcha:        captchaMW,
			LoginThrottle:  loginThrottle,
			ForgotThrottle: forgotThrottle,
		})},
		{pattern: "/u", handler: suppression.NewHandler(cfg.JWTSecret, suppStore).Routes()},
		// Recipients follow open-pixel/click-redirect links unauthenticated,
		// same as /u — mounted here, not the protected group.
		{pattern: "/t", handler: trackHandler.Routes()},
		// OAuth callbacks (Gmail, M365) are top-level browser navigations from
		// the provider; they authenticate from the signed state, not the JWT
		// cookie, so they mount here rather than the protected group.
		{pattern: "/oauth", handler: mbHandler.CallbackRoutes()},
	}
	// The data-plane REST surface accepts EITHER a session or an api-key credential.
	// The api-key verifier runs first and DEFERS on a non-`inrd_` token, so a JWT
	// falls through to the session verifier; an api-key principal is then attenuated
	// to its granted scopes by each route's RequireScope (a session holds all scopes).
	dataPlane := []mount{
		{pattern: "/api/v1/mailboxes", handler: mbHandler.Routes(identStore)},
		{pattern: "/api/v1/lists", handler: list.NewHandler(listSvc).Routes()},
		// Mounted at /api/v1/contacts (not /api/v1) to avoid the chi mount-prefix
		// overlap with /api/v1/lists that would otherwise 404 the import route.
		// Surface: POST /api/v1/contacts/import?list={id}, GET /api/v1/contacts?list={id}.
		{pattern: "/api/v1/contacts", handler: contact.NewHandler(contactSvc).Routes()},
		// Sequence steps register as a SubRouter under the campaigns mount, so
		// /campaigns/{id}/steps lives under this group and inherits its RequireAuth.
		// Routes(identStore) additionally applies RequireVerified to /launch
		// (email-gated sending).
		{pattern: "/api/v1/campaigns", handler: campaign.NewHandler(campaignSvc, enq, stepHandler).Routes(identStore)},
	}
	// Session-only surface: workspace administration and the warmup overview are not
	// part of the api-key contract, so they authenticate with the session verifier
	// alone — an `inrd_` token presented here is rejected 401 (fail closed).
	sessionOnly := []mount{
		{pattern: "/api/v1/workspaces", handler: identHandler.InviteRoutes()},
		// Workspace-level warmup overview. The per-mailbox warmup routes
		// (/mailboxes/{id}/warmup) are registered as a sub-router of the mailbox
		// mount above, not here.
		{pattern: "/api/v1/warmup", handler: warmupHandler.Routes()},
	}
	router := buildRouter(logger, public, []protectedGroup{
		{verifiers: []auth.Verifier{apiKeyVerifier, sessionVerifier}, mounts: dataPlane},
		{verifiers: []auth.Verifier{sessionVerifier}, mounts: sessionOnly},
	})

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
func buildRouter(logger *slog.Logger, public []mount, groups []protectedGroup) *chi.Mux {
	r := httpx.NewRouter(logger)
	for _, m := range public {
		r.Mount(m.pattern, m.handler)
	}
	for _, g := range groups {
		r.Group(func(pr chi.Router) {
			pr.Use(auth.RequireAuth(g.verifiers...))
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
