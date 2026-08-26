package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/notify"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run holds the seed logic so deferred cleanup (pool.Close) executes before the
// process exits; main translates a returned error into a non-zero exit code.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	sender, err := notify.New(notify.Config{}) // console driver: seed doesn't need real delivery
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	svc := identity.NewService(identity.NewStore(pool), time.Hour, sender, cfg.AppBaseURL,
		cfg.EmailVerifyTTL, cfg.PasswordResetTTL, cfg.InviteTTL)
	sess, err := svc.Register(ctx, identity.RegisterInput{
		WorkspaceName: "Demo Workspace",
		Email:         "demo@inroad.test",
		Password:      "demodemo",
		UserAgent:     "seed",
		IP:            "",
	})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	// Fixtures come second and deliberately do NOT roll the registration back on
	// failure: a workspace you can log into is worth more than an all-or-nothing
	// seed, and a re-run fails at Register anyway since the user already exists.
	summary, err := seedFixtures(ctx, pool, sess.WorkspaceID, sess.UserID)
	if err != nil {
		return fmt.Errorf("fixtures: %w", err)
	}
	fmt.Printf("seeded workspace=%s user=%s\n", sess.WorkspaceID, sess.UserID)
	fmt.Printf("  %s\n", summary)
	fmt.Printf("  login demo@inroad.test / demodemo\n")
	return nil
}
