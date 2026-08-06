//go:build integration

package testsend

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// This is the worker-side half of the security fix: docs/security.md requires
// a test-send to honor the SAME suppression list a real send does, and the
// API-side check in campaign.Service.TestSend can race an incoming
// unsubscribe between enqueue and this task running. These tests drive the
// REAL in-process coreapi client (Handler's actual production dependency,
// type-asserted onto testsend.Core exactly as internal/worker/handlers.go
// does) against Postgres -- not the stubCore fake used elsewhere in this
// package. Docker must be up.

// itMasterKey is this package's fixed 32-byte master key for the legacy
// Sealer that seeds the mailbox credential and the Keyring that opens it.
var itMasterKey = bytes.Repeat([]byte{13}, 32)

func itKeyring(t *testing.T, q *gen.Queries) *crypto.Keyring {
	t.Helper()
	kp, err := crypto.NewLocalKeyProvider(itMasterKey)
	if err != nil {
		t.Fatalf("key provider: %v", err)
	}
	legacy, err := crypto.NewSealer(itMasterKey)
	if err != nil {
		t.Fatalf("legacy sealer: %v", err)
	}
	return crypto.NewKeyring(kp, keys.NewPgDEKStore(q), legacy)
}

// itFixture is one seeded workspace/mailbox/campaign/step, ready to drive a
// real testsend:send task through the real coreapi client.
type itFixture struct {
	core       Core
	q          *gen.Queries
	ws         uuid.UUID
	campaignID uuid.UUID
	stepID     uuid.UUID
	mailboxID  uuid.UUID
}

func setupTestSendIT(t *testing.T) (context.Context, itFixture) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)

	sealer, err := crypto.NewSealer(itMasterKey)
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	ct, err := sealer.Seal([]byte("smtp-app-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	ws, err := q.CreateWorkspace(ctx, "TestSend Worker IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	email := "from-" + uuid.NewString() + "@acme.test"
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws.ID, Provider: "smtp", Email: email, DisplayName: "Acme",
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: ct, DailyCap: 100, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws.ID, Name: "Camp", MailboxID: mb.ID, ListID: lst.ID,
		Subject: "Hi", BodyText: "Hello",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	step, err := q.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: ws.ID, CampaignID: cam.ID, StepOrder: 1, DelaySeconds: 0,
		Subject: "Hi {{first_name}}", BodyText: "Hello", BodyHtml: "",
	})
	if err != nil {
		t.Fatalf("step: %v", err)
	}

	client := inprocess.New(pool, itKeyring(t, q), []byte("0123456789abcdef0123456789abcdef"),
		"https://app.test", mail.GoogleOAuth{}, mail.MicrosoftOAuth{},
		[]byte("warmup-secret-0123456789abcdef"), warmup.NewStaticLibrary())
	core, ok := client.(Core)
	if !ok {
		t.Fatal("the in-process coreapi client no longer satisfies testsend.Core")
	}

	return ctx, itFixture{core: core, q: q, ws: ws.ID, campaignID: cam.ID, stepID: step.ID, mailboxID: mb.ID}
}

func (f itFixture) task(t *testing.T, to string) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.TestSendPayload{
		CampaignID: f.campaignID.String(), StepID: f.stepID.String(),
		MailboxID: f.mailboxID.String(), To: to, WorkspaceID: f.ws.String(),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(queue.TaskTestSend, b)
}

// TestHandlerSkipsARealSuppressedRecipientWithoutSending is the required
// worker-side coverage: a REAL suppression row (seeded through the same
// AddSuppression query internal/app/suppression.Store.Add uses) must make the
// real coreapi client's IsSuppressed report true, and the handler must skip
// the send entirely -- no error (no retry storm) and nothing sent.
func TestHandlerSkipsARealSuppressedRecipientWithoutSending(t *testing.T) {
	ctx, f := setupTestSendIT(t)
	to := "unsub-" + uuid.NewString() + "@x.test"
	if err := f.q.AddSuppression(ctx, gen.AddSuppressionParams{
		WorkspaceID: f.ws, Email: to, Reason: "unsubscribe",
	}); err != nil {
		t.Fatalf("seed suppression: %v", err)
	}

	mailer := &stubMailer{}
	err := Handler(f.core, mailer)(ctx, f.task(t, to))
	if err != nil {
		t.Fatalf("Handler: %v, want nil (a suppressed recipient is skipped, not failed)", err)
	}
	if mailer.calls != 0 {
		t.Error("a real suppression row must block the send")
	}
}

// A non-suppressed address, checked against the real table, still sends --
// proving the worker-side re-check does not false-positive.
func TestHandlerStillSendsToARealUnsuppressedRecipient(t *testing.T) {
	ctx, f := setupTestSendIT(t)
	to := "clean-" + uuid.NewString() + "@x.test"

	mailer := &stubMailer{}
	if err := Handler(f.core, mailer)(ctx, f.task(t, to)); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if mailer.calls != 1 {
		t.Errorf("mailer calls = %d, want 1", mailer.calls)
	}
	if mailer.msg.To != to {
		t.Errorf("To = %q, want %q", mailer.msg.To, to)
	}
}
