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
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db:", err)
		os.Exit(1)
	}
	defer pool.Close()

	sender, err := notify.New(notify.Config{}) // console driver: seed doesn't need real delivery
	if err != nil {
		fmt.Fprintln(os.Stderr, "notify:", err)
		os.Exit(1)
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
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Printf("seeded workspace=%s user=%s (login demo@inroad.test / demodemo)\n", sess.WorkspaceID, sess.UserID)
}
