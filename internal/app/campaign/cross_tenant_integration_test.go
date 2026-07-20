//go:build integration

package campaign

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

// TestCrossTenantGetIsNotFound proves the "workspace_id in the WHERE clause"
// pin on GetCampaign is doing its job at the DB layer: workspace A asking
// for workspace B's campaign id must see pgx.ErrNoRows (which the handler
// maps to 404), not the leaked row.
func TestCrossTenantGetIsNotFound(t *testing.T) {
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := gen.New(pool)

	// Two independent workspaces. B owns a campaign; A tries to read it.
	wsA, err := q.CreateWorkspace(ctx, "A "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace A: %v", err)
	}
	wsB, err := q.CreateWorkspace(ctx, "B "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace B: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: wsB.ID, Provider: "smtp", Email: "b@x.test", DisplayName: "B",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: "b@x.test",
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: "b@x.test",
		SecretCiphertext: []byte("ct"), UseTls: true, DailyCap: 50,
		MinIntervalSeconds: 120, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: wsB.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: wsB.ID, Name: "Bs", MailboxID: mb.ID, ListID: lst.ID,
		Subject: "Hi", BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}

	// A asks for B's campaign — must be pgx.ErrNoRows (handler → 404).
	if _, err := q.GetCampaign(ctx, gen.GetCampaignParams{ID: cam.ID, WorkspaceID: wsA.ID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-tenant GetCampaign: want pgx.ErrNoRows, got %v", err)
	}

	// Stats: workspace filter forces empty rows on cross-tenant reads even
	// though the campaign id is real.
	rows, err := q.CountSendsByStatus(ctx, gen.CountSendsByStatusParams{CampaignID: cam.ID, WorkspaceID: wsA.ID})
	if err != nil {
		t.Fatalf("CountSendsByStatus: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("cross-tenant Stats leaked %d rows", len(rows))
	}

	// Sanity: B still sees its own campaign.
	got, err := q.GetCampaign(ctx, gen.GetCampaignParams{ID: cam.ID, WorkspaceID: wsB.ID})
	if err != nil {
		t.Fatalf("owning-tenant GetCampaign: %v", err)
	}
	if got.ID != cam.ID {
		t.Fatalf("owning-tenant read mismatch: got %s want %s", got.ID, cam.ID)
	}
}
