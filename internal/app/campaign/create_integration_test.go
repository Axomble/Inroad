//go:build integration

package campaign

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// TestCreateSeedsStepOne proves the create→launch bug fix at the DB layer:
// PgStore.Create must insert the campaign AND exactly one sequence_step (order
// 1, delay 0, subject/body mirrored) in a single transaction, so a UI-created
// campaign is immediately launchable (Launch requires >=1 step). The unit test
// covers the service against a fake store; this closes the live-DB gap on the
// transactional seed.
func TestCreateSeedsStepOne(t *testing.T) {
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
	store := NewPgStore(pool)

	w, err := q.CreateWorkspace(ctx, "Seed "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "from@x.test", DisplayName: "X",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: "from@x.test",
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: "from@x.test",
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
		Name: "Q3", Subject: "Hello there", BodyText: "body text", BodyHTML: "<p>body html</p>",
		MailboxID: mb.ID, ListID: lst.ID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	steps, err := q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: cam.ID, WorkspaceID: w.ID})
	if err != nil {
		t.Fatalf("ListStepsByCampaign: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("want exactly 1 seeded step, got %d", len(steps))
	}
	st := steps[0]
	switch {
	case st.StepOrder != 1:
		t.Errorf("step_order: got %d want 1", st.StepOrder)
	case st.DelaySeconds != 0:
		t.Errorf("delay_seconds: got %d want 0", st.DelaySeconds)
	case st.Subject != "Hello there":
		t.Errorf("subject: got %q want %q", st.Subject, "Hello there")
	case st.BodyText != "body text":
		t.Errorf("body_text: got %q want %q", st.BodyText, "body text")
	case st.BodyHtml != "<p>body html</p>":
		t.Errorf("body_html: got %q want %q", st.BodyHtml, "<p>body html</p>")
	case st.WorkspaceID != w.ID:
		t.Errorf("workspace_id: got %s want %s", st.WorkspaceID, w.ID)
	}
}
