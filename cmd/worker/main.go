package main

import (
	"context"
	"fmt"
	"os"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

// run wires and serves the worker. Keeping the body here (rather than in main)
// guarantees the deferred cleanups (pool.Close, enq.Close, sch.Shutdown) run
// before the process exits; main only maps a returned error to a non-zero
// status code. The error paths log/print their own diagnostics before returning.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		// Report before building the logger: config failure means we may not have
		// the info the logger needs (env/level), and matching cmd/migrate keeps
		// bad-config output uniform across binaries.
		fmt.Fprintln(os.Stderr, "config:", err)
		return err
	}
	logger := log.New(cfg.Env)

	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		return err
	}
	defer pool.Close()

	// Build the per-workspace Keyring at the worker's composition root. The
	// DEKStore is the sqlc-backed adapter over the pool; the worker engine
	// packages never see it — they reach data only through coreapi, which holds
	// the Keyring. keys.BuildKeyring owns the fail-closed provider guard.
	keyring, err := keys.BuildKeyring(cfg, gen.New(pool))
	if err != nil {
		logger.Error("keyring init failed", "err", err)
		return err
	}

	// The worker package depends only on coreapi.Client; the DB-backed
	// implementation is wired here at the composition root.
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
	core := inprocess.New(pool, keyring, cfg.JWTSecret, cfg.PublicURL, googleOAuth, msOAuth)
	// MultiSender dispatches SMTP vs Gmail vs Graph on the job's Provider; the
	// SMTP leg keeps the SSRF-vetted NetSender, the Gmail leg uses the fixed
	// Google host, and the m365 leg uses the fixed Microsoft Graph host.
	sndr := mail.NewMultiSender(mail.NewNetSender(cfg.MailAllowPrivateHosts), mail.NewGmailSender(), mail.NewGraphSender())
	reader := mail.NewNetInboxReader(cfg.MailAllowPrivateHosts)
	enq := queue.NewClient(cfg.RedisAddr)
	defer enq.Close()

	// Start the periodic scheduler alongside the worker. It enqueues
	// send:sweep_stuck every 2 minutes so orphaned sends (launch committed
	// DB rows but Redis enqueue failed) get retried without operator action.
	sch := queue.NewScheduler(cfg.RedisAddr, logger)
	if err := queue.RegisterSweepStuck(sch); err != nil {
		logger.Error("scheduler register failed", "err", err)
		return err
	}
	if err := queue.RegisterSweepEnrollments(sch); err != nil {
		logger.Error("scheduler register (enrollments) failed", "err", err)
		return err
	}
	if err := queue.RegisterInboxSweep(sch); err != nil {
		logger.Error("scheduler register (inbox sweep) failed", "err", err)
		return err
	}
	go func() {
		if err := sch.Run(); err != nil {
			logger.Error("scheduler exited", "err", err)
		}
	}()
	defer sch.Shutdown()

	srv := queue.NewServer(cfg.RedisAddr, logger, cfg.WorkerConcurrency)
	mux := queue.NewMux()
	worker.Register(mux, core, sndr, reader, enq, cfg.PublicURL, cfg.TrackingSecret)

	logger.Info("worker starting", "redis", cfg.RedisAddr, "concurrency", cfg.WorkerConcurrency)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		return err
	}
	return nil
}
