package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/inroad/inroad/internal/app/workspace"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/log"
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

	queries := gen.New(pool)
	wsHandler := workspace.NewHandler(workspace.NewService(workspace.NewStore(queries)), cfg.JWTSecret)

	router := httpx.NewRouter(logger)
	router.Mount("/api/v1/workspaces", wsHandler.Routes())

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
