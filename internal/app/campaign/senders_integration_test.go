//go:build integration

package campaign

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

// senderFixture spins up a workspace with two mailboxes and a campaign whose
// mailbox is the first, returning the live store plus the ids. The campaign is
// created through the store, so it has the default schedule but no pool rows.
type senderFixture struct {
	store      *PgStore
	q          *gen.Queries
	pool       *pgxpool.Pool
	ws         uuid.UUID
	campaignID uuid.UUID
	mailboxA   uuid.UUID
	mailboxB   uuid.UUID
}

func setupSenders(t *testing.T, ctx context.Context) senderFixture {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	store := NewPgStore(pool)

	w, err := q.CreateWorkspace(ctx, "Senders "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mailbox := func(email string) uuid.UUID {
		mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
			WorkspaceID: w.ID, Provider: "smtp", Email: email, DisplayName: email,
			SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: email,
			ImapHost: "imap.x", ImapPort: 993, ImapUsername: email,
			SecretCiphertext: "ct", DailyCap: 100,
			MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
		})
		if err != nil {
			t.Fatalf("mailbox %s: %v", email, err)
		}
		return mb.ID
	}
	a := mailbox("a-" + uuid.NewString() + "@x.test")
	b := mailbox("b-" + uuid.NewString() + "@x.test")

	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: w.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cam, err := store.Create(ctx, w.ID, CreateInput{
		Name: "Pool", Subject: "Hi", BodyText: "b", MailboxID: a, ListID: lst.ID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return senderFixture{store: store, q: q, pool: pool, ws: w.ID, campaignID: cam.ID, mailboxA: a, mailboxB: b}
}

// A campaign with no pool rows must read as the implicit one-mailbox pool from
// campaigns.mailbox_id, not as an empty pool.
func TestFallbackSenderProjectsTheCampaignMailbox(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)

	senders, err := f.store.ListSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSenders: %v", err)
	}
	if len(senders) != 0 {
		t.Fatalf("pool rows = %d, want 0 for this fixture", len(senders))
	}
	fallback, err := f.store.FallbackSender(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("FallbackSender: %v", err)
	}
	if fallback.MailboxID != f.mailboxA {
		t.Errorf("fallback mailbox = %s, want %s", fallback.MailboxID, f.mailboxA)
	}
	if fallback.Weight != defaultSenderWeight || !fallback.Enabled || fallback.Status != "active" {
		t.Errorf("fallback = %+v, want weight 1, enabled, active", fallback)
	}
	if fallback.LastAssignedAt != nil {
		t.Errorf("fallback carries rotation state it cannot have: %v", fallback.LastAssignedAt)
	}
}

// The counter-preservation guarantee: replacing the pool must not reset
// assigned_count/last_assigned_at for a mailbox that stays in it, or every weight
// edit would restart the rotation.
func TestReplaceSendersPreservesRotationCountersForRetainedMailboxes(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)

	if err := f.store.ReplaceSenders(ctx, f.ws, f.campaignID, rotation.ModeRoundRobin, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 1, Enabled: true},
		{MailboxID: f.mailboxB, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceSenders: %v", err)
	}
	// Simulate rotation having happened for both members.
	for _, id := range []uuid.UUID{f.mailboxA, f.mailboxB} {
		if err := f.q.BumpCampaignSenderAssignment(ctx, gen.BumpCampaignSenderAssignmentParams{
			CampaignID: f.campaignID, WorkspaceID: f.ws, MailboxID: id,
		}); err != nil {
			t.Fatalf("bump %s: %v", id, err)
		}
	}

	// Edit A's weight and drop B.
	if err := f.store.ReplaceSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 50, Enabled: false},
	}); err != nil {
		t.Fatalf("second ReplaceSenders: %v", err)
	}
	senders, err := f.store.ListSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSenders: %v", err)
	}
	if len(senders) != 1 || senders[0].MailboxID != f.mailboxA {
		t.Fatalf("pool = %+v, want only mailbox A", senders)
	}
	got := senders[0]
	if got.Weight != 50 || got.Enabled {
		t.Errorf("editable fields not applied: %+v", got)
	}
	if got.AssignedCount != 1 || got.LastAssignedAt == nil {
		t.Errorf("rotation state was reset by the replace: %+v", got)
	}
	// And the mode moved with it.
	cam, err := f.q.GetCampaign(ctx, gen.GetCampaignParams{ID: f.campaignID, WorkspaceID: f.ws})
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	if cam.RotationMode != rotation.ModeWeighted {
		t.Errorf("rotation_mode = %q, want %q", cam.RotationMode, rotation.ModeWeighted)
	}
}

// Cross-tenant reads return nothing and a cross-tenant replace must not touch the
// owner's pool, even with a correct campaign id.
func TestSenderQueriesArePinnedToTheWorkspace(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)
	other, err := f.q.CreateWorkspace(ctx, "Intruder "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if err := f.store.ReplaceSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 7, Enabled: true},
	}); err != nil {
		t.Fatalf("seed pool: %v", err)
	}

	senders, err := f.store.ListSenders(ctx, other.ID, f.campaignID)
	if err != nil {
		t.Fatalf("cross-tenant ListSenders: %v", err)
	}
	if len(senders) != 0 {
		t.Errorf("cross-tenant ListSenders returned %d rows", len(senders))
	}
	if _, err := f.store.FallbackSender(ctx, other.ID, f.campaignID); err == nil {
		t.Error("cross-tenant FallbackSender resolved a mailbox")
	}

	// The cross-tenant replace fails (the tenant FK cannot match) or writes
	// nothing; either way the owner's pool must survive untouched.
	_ = f.store.ReplaceSenders(ctx, other.ID, f.campaignID, rotation.ModeRoundRobin, []SenderInput{
		{MailboxID: f.mailboxB, Weight: 1, Enabled: true},
	})
	owned, err := f.store.ListSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSenders: %v", err)
	}
	if len(owned) != 1 || owned[0].MailboxID != f.mailboxA || owned[0].Weight != 7 {
		t.Errorf("owner's pool = %+v after a cross-tenant replace, want the original", owned)
	}
}

// Belt-and-braces behind the service's 422: the composite tenant FK makes a
// mailbox from another workspace unrepresentable in a campaign's pool.
func TestPoolRejectsAMailboxFromAnotherWorkspace(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)
	other, err := f.q.CreateWorkspace(ctx, "Other "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	foreign, err := f.q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: other.ID, Provider: "smtp", Email: "foreign@x.test", DisplayName: "F",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: "foreign@x.test",
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: "foreign@x.test",
		SecretCiphertext: "ct", DailyCap: 100,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("foreign mailbox: %v", err)
	}

	if err := f.store.ReplaceSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: foreign.ID, Weight: 1, Enabled: true},
	}); err == nil {
		t.Fatal("the database accepted a mailbox from another workspace into the pool")
	}
	senders, err := f.store.ListSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSenders: %v", err)
	}
	if len(senders) != 0 {
		t.Errorf("the failed replace left %d rows behind", len(senders))
	}
}

// The full-replace flow through the service: validation, write, and the re-read
// response, against Postgres.
func TestSetSendersEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)
	svc := NewService(f.store, alwaysOKChecker{})

	pool, err := svc.SetSenders(ctx, f.ws, f.campaignID, rotation.ModeLRU, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 3, Enabled: true},
		{MailboxID: f.mailboxB, Weight: 1, Enabled: false},
	})
	if err != nil {
		t.Fatalf("SetSenders: %v", err)
	}
	if pool.RotationMode != rotation.ModeLRU {
		t.Errorf("mode = %q, want %q", pool.RotationMode, rotation.ModeLRU)
	}
	if len(pool.Senders) != 2 {
		t.Fatalf("senders = %d, want 2", len(pool.Senders))
	}
	for _, s := range pool.Senders {
		if s.Email == "" || s.Status == "" {
			t.Errorf("sender %+v is missing the read-only mailbox identity", s)
		}
		if s.AssignedCount != 0 || s.LastAssignedAt != nil {
			t.Errorf("fresh pool member carries rotation state: %+v", s)
		}
	}
	// Reading it back returns the real rows, not the fallback.
	read, err := svc.GetSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if len(read.Senders) != 2 {
		t.Errorf("read-back senders = %d, want 2", len(read.Senders))
	}
}
