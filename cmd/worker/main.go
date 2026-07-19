package main

import (
	"context"
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
	logger := log.New("development")
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	logger = log.New(cfg.Env)

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

	srv := queue.NewServer(cfg.RedisAddr, logger)
	mux := queue.NewMux()
	worker.Register(mux, core, sndr, enq)

	logger.Info("worker starting", "redis", cfg.RedisAddr)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		os.Exit(1)
	}
}
