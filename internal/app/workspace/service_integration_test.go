//go:build integration

package workspace

import (
	"context"
	"os"
	"testing"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

func newService(t *testing.T) *Service {
	t.Helper()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewService(NewStore(gen.New(pool)))
}

func TestRegisterThenAuthenticate(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	res, err := svc.Register(ctx, RegisterInput{
		WorkspaceName: "Acme",
		Email:         "founder@acme.test",
		Password:      "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if res.WorkspaceID == "" || res.UserID == "" {
		t.Fatalf("empty ids: %+v", res)
	}

	uid, wid, err := svc.Authenticate(ctx, "founder@acme.test", "hunter2hunter2")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if uid != res.UserID || wid != res.WorkspaceID {
		t.Errorf("auth ids mismatch: got (%s,%s) want (%s,%s)", uid, wid, res.UserID, res.WorkspaceID)
	}

	if _, _, err := svc.Authenticate(ctx, "founder@acme.test", "wrong"); err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}
