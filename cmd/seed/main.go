package main

import (
	"context"
	"fmt"
	"os"

	"github.com/inroad/inroad/internal/app/workspace"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
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

	svc := workspace.NewService(workspace.NewStore(gen.New(pool)))
	res, err := svc.Register(ctx, workspace.RegisterInput{
		WorkspaceName: "Demo Workspace",
		Email:         "demo@inroad.test",
		Password:      "demodemo",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Printf("seeded workspace=%s user=%s (login demo@inroad.test / demodemo)\n", res.WorkspaceID, res.UserID)
}
