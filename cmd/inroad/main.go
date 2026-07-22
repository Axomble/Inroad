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

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/app/contact"
	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/app/mailbox"
	"github.com/inroad/inroad/internal/app/suppression"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/notify"
	"github.com/inroad/inroad/internal/platform/queue"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Exit before building the logger: config failure could hide log
		// options; matching cmd/migrate keeps bad-config output uniform.
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := log.New(cfg.Env)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	sealer, err := crypto.NewSealer(cfg.MasterKey)
	if err != nil {
		logger.Error("sealer init failed", "err", err)
		os.Exit(1)
	}

	sender, err := notify.New(notify.Config{
		Driver: cfg.TransactionalDriver, SMTPHost: cfg.SystemSMTPHost, SMTPPort: cfg.SystemSMTPPort,
		SMTPUsername: cfg.SystemSMTPUsername, SMTPPassword: cfg.SystemSMTPPassword, From: cfg.SystemEmailFrom,
		Logger: logger,
	})
	if err != nil {
		logger.Error("transactional sender init failed", "err", err)
		os.Exit(1)
	}

	queries := gen.New(pool)
	identStore := identity.NewStore(pool)
	identHandler := identity.NewHandler(
		identity.NewService(identStore, cfg.RefreshTokenTTL, sender, cfg.AppBaseURL,
			cfg.EmailVerifyTTL, cfg.PasswordResetTTL, cfg.InviteTTL),
		cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL, cfg.CookieSecure, cfg.CookieDomain,
		cfg.TrustedProxies,
	)
	mailboxStore := mailbox.NewPgStore(queries)
	mbHandler := mailbox.NewHandler(
		mailbox.NewService(mailboxStore, mail.NewNetTester(cfg.MailAllowPrivateHosts), sealer),
	)

	enq := queue.NewClient(cfg.RedisAddr)
	defer enq.Close()
	listSvc := list.NewService(list.NewPgStore(queries))
	// contact takes only a small ListChecker interface (not the whole list
	// service) so the contact package doesn't have to import app/list —
	// keeps the "app packages don't import each other" invariant intact.
	contactSvc := contact.NewService(contact.NewPgStore(queries), listCheckerAdapter{lists: listSvc})
	// checker adapts the mailbox and list stores for campaign ownership checks.
	campaignSvc := campaign.NewService(campaign.NewPgStore(pool), ownershipChecker{mailboxes: mailboxStore, lists: listSvc})
	suppStore := suppression.NewStore(queries)

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
		{pattern: "/api/v1/auth", handler: identHandler.Routes(cfg.JWTSecret)},
		{pattern: "/u", handler: suppression.NewHandler(cfg.JWTSecret, suppStore).Routes()},
	}
	protected := []mount{
		{pattern: "/api/v1/mailboxes", handler: mbHandler.Routes(identStore)},
		{pattern: "/api/v1/lists", handler: list.NewHandler(listSvc).Routes()},
		// Mounted at /api/v1/contacts (not /api/v1) to avoid the chi mount-prefix
		// overlap with /api/v1/lists that would otherwise 404 the import route.
		// Surface: POST /api/v1/contacts/import?list={id}, GET /api/v1/contacts?list={id}.
		{pattern: "/api/v1/contacts", handler: contact.NewHandler(contactSvc).Routes()},
		{pattern: "/api/v1/campaigns", handler: campaign.NewHandler(campaignSvc, enq).Routes(identStore)},
	}
	router := buildRouter(logger, cfg.JWTSecret, public, protected)

	srv := httpx.NewServer(cfg.HTTPAddr, router)
	logger.Info("api listening", "addr", cfg.HTTPAddr)
	if err := httpx.Run(ctx, srv); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}

// mount pairs a URL prefix with the handler served under it.
type mount struct {
	pattern string
	handler http.Handler
}

// buildRouter assembles the API router with a deny-by-default posture. Public
// mounts are served as-is; every protected mount is wrapped once, at the group
// root, by auth.RequireAuth(secret). A route added under the protected group is
// therefore authenticated whether or not it wires up any middleware of its own
// -- forgetting a per-domain guard can no longer expose a route.
func buildRouter(logger *slog.Logger, secret []byte, public, protected []mount) *chi.Mux {
	r := httpx.NewRouter(logger)
	for _, m := range public {
		r.Mount(m.pattern, m.handler)
	}
	r.Group(func(pr chi.Router) {
		pr.Use(auth.RequireAuth(secret))
		for _, m := range protected {
			pr.Mount(m.pattern, m.handler)
		}
	})
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
