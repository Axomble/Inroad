//go:build integration

package webhook_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/webhook"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/keys"
)

// These exercise migration 20260907132955 against Postgres: the workspace pin on
// every read/write, and the delivery-log keyset pagination. Docker must be up.

func connect(t *testing.T) (*pgxpool.Pool, *gen.Queries) {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, gen.New(pool)
}

func testKeyring(t *testing.T, pool *pgxpool.Pool) *crypto.Keyring {
	t.Helper()
	kp, err := crypto.NewLocalKeyProvider(make([]byte, 32))
	if err != nil {
		t.Fatalf("key provider: %v", err)
	}
	legacy, err := crypto.NewSealer(make([]byte, 32))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	return crypto.NewKeyring(kp, keys.NewPgDEKStore(gen.New(pool)), legacy)
}

type noopEnq struct{}

func (noopEnq) EnqueueWebhookDeliver(string, string) error { return nil }

func newSvc(t *testing.T, pool *pgxpool.Pool) *webhook.Service {
	t.Helper()
	return webhook.NewService(webhook.NewPgStore(gen.New(pool)), testKeyring(t, pool), noopEnq{}, false)
}

// A second workspace can neither read, patch, nor delete another workspace's
// endpoint — every query is workspace-pinned.
func TestEndpointCRUDIsWorkspaceScoped(t *testing.T) {
	ctx := context.Background()
	pool, q := connect(t)
	wsA, err := q.CreateWorkspace(ctx, "Webhook IT A "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace A: %v", err)
	}
	wsB, err := q.CreateWorkspace(ctx, "Webhook IT B "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace B: %v", err)
	}
	svc := newSvc(t, pool)

	ep, secret, err := svc.Create(ctx, wsA.ID, webhook.CreateInput{URL: "https://8.8.8.8/hook"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if secret == "" {
		t.Fatal("Create returned an empty secret")
	}

	if _, err := svc.Get(ctx, wsB.ID, ep.ID); err == nil {
		t.Fatal("workspace B could read workspace A's endpoint")
	}
	active := false
	if _, err := svc.Update(ctx, wsB.ID, ep.ID, webhook.UpdateInput{Active: &active}); err == nil {
		t.Fatal("workspace B could patch workspace A's endpoint")
	}
	if err := svc.Delete(ctx, wsB.ID, ep.ID); err == nil {
		t.Fatal("workspace B could delete workspace A's endpoint")
	}

	// A's own reads still work.
	if _, err := svc.Get(ctx, wsA.ID, ep.ID); err != nil {
		t.Fatalf("owner Get: %v", err)
	}
	list, err := svc.List(ctx, wsB.ID)
	if err != nil {
		t.Fatalf("List B: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("workspace B sees %d endpoints, want 0", len(list))
	}
}

// The delivery log is workspace-pinned and keyset pagination returns stable,
// non-overlapping pages.
func TestDeliveryKeysetPagination(t *testing.T) {
	ctx := context.Background()
	pool, q := connect(t)
	wsA, err := q.CreateWorkspace(ctx, "Webhook IT deliv A "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	wsB, err := q.CreateWorkspace(ctx, "Webhook IT deliv B "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	svc := newSvc(t, pool)

	ep, _, err := svc.Create(ctx, wsA.ID, webhook.CreateInput{URL: "https://8.8.8.8/hook"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const total = 12
	for i := 0; i < total; i++ {
		if _, err := svc.Ping(ctx, wsA.ID, ep.ID); err != nil {
			t.Fatalf("Ping %d: %v", i, err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		page, err := svc.ListDeliveries(ctx, wsA.ID, ep.ID, cursor, 5)
		if err != nil {
			t.Fatalf("ListDeliveries: %v", err)
		}
		pages++
		for _, d := range page.Items {
			if seen[d.ID.String()] {
				t.Fatalf("delivery %s appeared on two pages", d.ID)
			}
			seen[d.ID.String()] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != total {
		t.Fatalf("paged over %d deliveries, want %d", len(seen), total)
	}

	// Workspace B cannot list workspace A's endpoint deliveries.
	if _, err := svc.ListDeliveries(ctx, wsB.ID, ep.ID, "", 5); err == nil {
		t.Fatal("workspace B listed workspace A's deliveries")
	}
}
