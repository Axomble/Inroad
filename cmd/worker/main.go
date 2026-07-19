package main

import (
	"os"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
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

	srv := queue.NewServer(cfg.RedisAddr, logger)
	mux := queue.NewMux()
	worker.Register(mux, inprocess.New())

	logger.Info("worker starting", "redis", cfg.RedisAddr)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		os.Exit(1)
	}
}
