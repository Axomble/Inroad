package main

import (
	"context"
	"fmt"
	"os"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// Exit before building the logger: config failure means we may not have
		// the info the logger needs (env/level), and matching cmd/migrate keeps
		// bad-config output uniform across binaries.
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := log.New(cfg.Env)

	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
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

	// The worker package depends only on coreapi.Client; the DB-backed
	// implementation is wired here at the composition root.
	core := inprocess.New(gen.New(pool), sealer, cfg.JWTSecret, cfg.PublicURL)
	sndr := mail.NewNetSender(cfg.MailAllowPrivateHosts)
	enq := queue.NewClient(cfg.RedisAddr)
	defer enq.Close()

	// Start the periodic scheduler alongside the worker. It enqueues
	// send:sweep_stuck every 2 minutes so orphaned sends (launch committed
	// DB rows but Redis enqueue failed) get retried without operator action.
	sch := queue.NewScheduler(cfg.RedisAddr, logger)
	if err := queue.RegisterSweepStuck(sch); err != nil {
		logger.Error("scheduler register failed", "err", err)
		os.Exit(1)
	}
	if err := queue.RegisterSweepEnrollments(sch); err != nil {
		logger.Error("scheduler register (enrollments) failed", "err", err)
		os.Exit(1)
	}
	go func() {
		if err := sch.Run(); err != nil {
			logger.Error("scheduler exited", "err", err)
		}
	}()
	defer sch.Shutdown()

	srv := queue.NewServer(cfg.RedisAddr, logger, cfg.WorkerConcurrency)
	mux := queue.NewMux()
	worker.Register(mux, core, sndr, enq)

	logger.Info("worker starting", "redis", cfg.RedisAddr, "concurrency", cfg.WorkerConcurrency)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		os.Exit(1)
	}
}
