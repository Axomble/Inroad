//go:build integration

package campaign

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

// TestDeleteDraftRemovesEveryDependent proves PgStore.DeleteDraft's explicit
// child deletes actually clear every dependent table (send windows,
// campaign_senders, sequence_steps, sequence_enrollments) and the campaign row
// itself, against real Postgres -- the FK cascade the brief says NOT to rely
// on would also produce this outcome, so this closes the live-DB gap on the
// explicit deletes actually being correct (right table, right params) rather
// than merely on cascade doing the work for them.
func TestDeleteDraftRemovesEveryDependent(t *testing.T) {
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := gen.New(pool)
	store := NewPgStore(pool)

	w, err := q.CreateWorkspace(ctx, "DeleteDraft "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mbA, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "a@x.test", DisplayName: "A",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: "a@x.test",
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: "a@x.test",
		SecretCiphertext: "ct", DailyCap: 50,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: w.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// store.Create seeds the default weekly schedule (5 send windows) and step 1
	// in the same transaction -- see store.go -- so this draft already carries
	// two of the four dependent tables before anything else is added.
	cam, err := store.Create(ctx, w.ID, CreateInput{
		Name: "Doomed", Subject: "Hi", BodyText: "hello", MailboxID: mbA.ID, ListID: lst.ID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A second sequence step, so ListStepsByCampaign has more than the seeded one.
	if _, err := q.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: w.ID, CampaignID: cam.ID, StepOrder: 2, DelaySeconds: 3600,
		Subject: "Follow-up", BodyText: "still there?",
	}); err != nil {
		t.Fatalf("second step: %v", err)
	}

	// A sender-pool row.
	if err := store.ReplaceSenders(ctx, w.ID, cam.ID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: mbA.ID, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceSenders: %v", err)
	}

	// An enrollment, materialized directly (EnrollListMembers alone does not
	// flip campaign status the way EnrollTx does), so the draft carries a row in
	// every one of the four dependent tables the brief lists.
	ct, err := q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: w.ID, Email: "lead@x.test", FirstName: "Lead"})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: lst.ID, ContactID: ct.ID}); err != nil {
		t.Fatalf("list member: %v", err)
	}
	if _, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: cam.ID, WorkspaceID: w.ID}); err != nil {
		t.Fatalf("EnrollListMembers: %v", err)
	}

	// Sanity: every dependent table actually has a row before deleting.
	if steps, _ := q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(steps) != 2 {
		t.Fatalf("precondition: want 2 steps, got %d", len(steps))
	}
	if windows, _ := q.ListSendWindows(ctx, gen.ListSendWindowsParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(windows) == 0 {
		t.Fatal("precondition: want the default send windows seeded, got none")
	}
	if senders, _ := q.ListCampaignSenders(ctx, gen.ListCampaignSendersParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(senders) != 1 {
		t.Fatalf("precondition: want 1 sender, got %d", len(senders))
	}
	if enr, _ := q.CountEnrollmentsByStatus(ctx, gen.CountEnrollmentsByStatusParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(enr) == 0 {
		t.Fatal("precondition: want at least one enrollment, got none")
	}

	if err := store.DeleteDraft(ctx, w.ID, cam.ID); err != nil {
		t.Fatalf("DeleteDraft: %v", err)
	}

	if _, err := q.GetCampaign(ctx, gen.GetCampaignParams{ID: cam.ID, WorkspaceID: w.ID}); err == nil {
		t.Error("campaign row still exists after DeleteDraft")
	}
	if steps, _ := q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(steps) != 0 {
		t.Errorf("sequence_steps not cleared: %d rows remain", len(steps))
	}
	if windows, _ := q.ListSendWindows(ctx, gen.ListSendWindowsParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(windows) != 0 {
		t.Errorf("campaign_send_windows not cleared: %d rows remain", len(windows))
	}
	if senders, _ := q.ListCampaignSenders(ctx, gen.ListCampaignSendersParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(senders) != 0 {
		t.Errorf("campaign_senders not cleared: %d rows remain", len(senders))
	}
	if enr, _ := q.CountEnrollmentsByStatus(ctx, gen.CountEnrollmentsByStatusParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(enr) != 0 {
		t.Errorf("sequence_enrollments not cleared: %v", enr)
	}
}

// TestDeleteDraftGuardRejectsNonDraftAndRollsBack proves the SQL-level
// status='draft' guard on DeleteDraftCampaign is real, not just the service's
// pre-check: calling PgStore.DeleteDraft directly (bypassing Service.DeleteDraft)
// on a launched campaign must roll back the whole transaction, leaving every
// dependent row untouched, and return ErrNotDraft.
func TestDeleteDraftGuardRejectsNonDraftAndRollsBack(t *testing.T) {
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	q := gen.New(pool)
	store := NewPgStore(pool)
	svc := NewService(store, alwaysOKChecker{})

	w, err := q.CreateWorkspace(ctx, "NotDraft "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "a@x.test", DisplayName: "A",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: "a@x.test",
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: "a@x.test",
		SecretCiphertext: "ct", DailyCap: 50,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: w.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cam, err := store.Create(ctx, w.ID, CreateInput{
		Name: "Launched", Subject: "Hi", BodyText: "hello", MailboxID: mb.ID, ListID: lst.ID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ct, err := q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: w.ID, Email: "lead@x.test", FirstName: "Lead"})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: lst.ID, ContactID: ct.ID}); err != nil {
		t.Fatalf("list member: %v", err)
	}
	// noopEnqueuer is defined in schedule_integration_test.go (same package);
	// Launch's actual scheduling behaviour is exercised there, this test only
	// needs Launch to flip the campaign to running.
	if _, err := svc.Launch(ctx, w.ID, cam.ID, noopEnqueuer{}); err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if err := store.DeleteDraft(ctx, w.ID, cam.ID); !errors.Is(err, ErrNotDraft) {
		t.Fatalf("DeleteDraft on a running campaign: got %v, want ErrNotDraft", err)
	}

	if _, err := q.GetCampaign(ctx, gen.GetCampaignParams{ID: cam.ID, WorkspaceID: w.ID}); err != nil {
		t.Errorf("campaign row was removed despite the rejected delete: %v", err)
	}
	if steps, _ := q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(steps) != 1 {
		t.Errorf("sequence_steps changed by the rejected delete: %d rows", len(steps))
	}
	if windows, _ := q.ListSendWindows(ctx, gen.ListSendWindowsParams{CampaignID: cam.ID, WorkspaceID: w.ID}); len(windows) == 0 {
		t.Error("campaign_send_windows was cleared despite the rejected delete")
	}
}
