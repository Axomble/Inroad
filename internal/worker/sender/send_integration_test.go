//go:build integration

package sender

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

type fakeSender struct{ sent []mail.Message }

func (f *fakeSender) Send(_ context.Context, _ mail.OutboundJob, msg mail.Message) (string, error) {
	f.sent = append(f.sent, msg)
	return "<test-message-id@inroad>", nil
}

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

func redisAddr() string {
	if v := os.Getenv("INROAD_REDIS_ADDR"); v != "" {
		return v
	}
	return "localhost:6379"
}

// TestSendPipelineEndToEnd exercises the whole send path against real Postgres:
// seed -> campaign launch (EnqueueSends) -> GetSendJob (real credential decrypt +
// cap) -> personalize + unsubscribe -> Send (faked) -> MarkSend. Only the SMTP
// wire is mocked; everything else is real.
func TestSendPipelineEndToEnd(t *testing.T) {
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

	masterKey := bytes.Repeat([]byte{7}, 32)
	sealer, err := crypto.NewSealer(masterKey)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	ct, err := sealer.Seal([]byte("smtp-app-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	ws, err := q.CreateWorkspace(ctx, "Send IT")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws.ID, Provider: "smtp", Email: "from@acme.test", DisplayName: "Acme",
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: "from@acme.test",
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: "from@acme.test",
		SecretCiphertext: ct, UseTls: true, DailyCap: 50, MinIntervalSeconds: 120,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	c1, err := q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: ws.ID, Email: "alice@x.test", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("contact1: %v", err)
	}
	c2, err := q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: ws.ID, Email: "bob@x.test", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("contact2: %v", err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: lst.ID, ContactID: c1.ID}); err != nil {
		t.Fatalf("member1: %v", err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: lst.ID, ContactID: c2.ID}); err != nil {
		t.Fatalf("member2: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws.ID, Name: "Camp", MailboxID: mb.ID, ListID: lst.ID,
		Subject: "Hi {{first_name}}", BodyText: "Hello {{first_name}} at {{email}}", BodyHtml: "",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}

	sendIDs, err := q.EnqueueSends(ctx, gen.EnqueueSendsParams{ID: cam.ID, WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("enqueue sends: %v", err)
	}
	if len(sendIDs) != 2 {
		t.Fatalf("expected 2 sends enqueued, got %d", len(sendIDs))
	}

	core := inprocess.New(pool, sealer, []byte("0123456789abcdef0123456789abcdef"), "https://app.test", mail.GoogleOAuth{})
	fs := &fakeSender{}
	enq := queue.NewClient(redisAddr())
	defer enq.Close()
	handler := Handler(core, fs, enq, "https://app.test", []byte("0123456789abcdef0123456789abcdef"))

	for _, id := range sendIDs {
		payload, _ := json.Marshal(queue.SendEmailPayload{SendID: id.String(), WorkspaceID: ws.ID.String()})
		if err := handler(ctx, asynq.NewTask(queue.TaskSendEmail, payload)); err != nil {
			t.Fatalf("handler: %v", err)
		}
	}

	// The fake sender received two personalized messages with unsubscribe wiring.
	if len(fs.sent) != 2 {
		t.Fatalf("expected 2 messages sent, got %d", len(fs.sent))
	}
	sawAlice := false
	for _, m := range fs.sent {
		if m.Subject == "Hi Alice" {
			sawAlice = true
		}
		if !strings.Contains(m.BodyText, "Unsubscribe:") {
			t.Errorf("body missing unsubscribe footer: %q", m.BodyText)
		}
		if m.ListUnsubscribe == "" {
			t.Errorf("missing List-Unsubscribe URL")
		}
	}
	if !sawAlice {
		t.Errorf("expected personalized subject 'Hi Alice' among sent messages")
	}

	// Every send row for the campaign is now 'sent'.
	rows, err := q.CountSendsByStatus(ctx, gen.CountSendsByStatusParams{CampaignID: cam.ID, WorkspaceID: ws.ID})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	sent := int64(0)
	for _, r := range rows {
		if r.Status == "sent" {
			sent = r.N
		}
	}
	if sent != 2 {
		t.Fatalf("expected 2 sends marked 'sent', got %d (rows=%v)", sent, rows)
	}
}
