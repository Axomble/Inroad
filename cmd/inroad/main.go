package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"

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
	"github.com/inroad/inroad/internal/platform/queue"
)

func main() {
	cfg, err := config.Load()
	logger := log.New(cfgEnv(cfg))
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

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

	queries := gen.New(pool)
	identHandler := identity.NewHandler(
		identity.NewService(identity.NewStore(pool), cfg.RefreshTokenTTL),
		cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL, cfg.CookieSecure, cfg.CookieDomain,
	)
	mailboxStore := mailbox.NewPgStore(queries)
	mbHandler := mailbox.NewHandler(
		mailbox.NewService(mailboxStore, mail.NewNetTester(cfg.MailAllowPrivateHosts), sealer),
		cfg.JWTSecret,
	)

	enq := queue.NewClient(cfg.RedisAddr)
	defer enq.Close()
	listSvc := list.NewService(list.NewPgStore(queries))
	contactSvc := contact.NewService(contact.NewPgStore(queries), listSvc)
	// checker adapts the mailbox and list stores for campaign ownership checks.
	campaignSvc := campaign.NewService(campaign.NewPgStore(pool), ownershipChecker{mailboxes: mailboxStore, lists: listSvc})
	suppStore := suppression.NewStore(queries)

	router := httpx.NewRouter(logger)
	router.Mount("/api/v1/auth", identHandler.Routes(cfg.JWTSecret))
	router.Mount("/api/v1/mailboxes", mbHandler.Routes())
	router.Mount("/api/v1/lists", list.NewHandler(listSvc, cfg.JWTSecret).Routes())
	// Mounted at /api/v1/contacts (not /api/v1) to avoid the chi mount-prefix
	// overlap with /api/v1/lists that would otherwise 404 the import route.
	// Surface: POST /api/v1/contacts/import?list={id}, GET /api/v1/contacts?list={id}.
	router.Mount("/api/v1/contacts", contact.NewHandler(contactSvc, cfg.JWTSecret).Routes())
	router.Mount("/api/v1/campaigns", campaign.NewHandler(campaignSvc, cfg.JWTSecret, enq).Routes())
	router.Mount("/u", suppression.NewHandler(cfg.JWTSecret, suppStore).Routes())

	srv := httpx.NewServer(cfg.HTTPAddr, router)
	logger.Info("api listening", "addr", cfg.HTTPAddr)
	if err := httpx.Run(ctx, srv); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}

// cfgEnv tolerates a nil config so we can still build a logger for the error path.
func cfgEnv(cfg *config.Config) string {
	if cfg == nil {
		return "development"
	}
	return cfg.Env
}

// ownershipChecker adapts the mailbox and list stores to campaign.Checker so
// campaign creation/launch can verify cross-domain references belong to the
// caller's workspace without the campaign package importing those domains.
type ownershipChecker struct {
	mailboxes mailbox.Store
	lists     *list.Service
}

// MailboxActive reports whether mailboxID exists in the workspace and is
// active. A missing mailbox is reported as inactive, not a hard error, so a
// bad/foreign id simply fails the ownership check.
func (o ownershipChecker) MailboxActive(ctx context.Context, ws, mailboxID uuid.UUID) (bool, error) {
	m, err := o.mailboxes.Get(ctx, ws, mailboxID)
	if err != nil {
		return false, nil
	}
	return m.Status == "active", nil
}

// ListExists reports whether listID exists in the workspace.
func (o ownershipChecker) ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error) {
	_, err := o.lists.Get(ctx, ws, listID)
	return err == nil, nil
}
