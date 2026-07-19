package main

import (
	"context"
	"os"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/log"
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

	// The worker package depends only on coreapi.Client; the DB-backed
	// implementation is wired here at the composition root.
	core := inprocess.New(gen.New(pool))

	srv := queue.NewServer(cfg.RedisAddr, logger)
	mux := queue.NewMux()
	worker.Register(mux, core)

	logger.Info("worker starting", "redis", cfg.RedisAddr)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		os.Exit(1)
	}
}
