//go:build integration

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/deliverability"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/warmup"
)

var registerMasterKey = bytes.Repeat([]byte{9}, 32)

// Register wires handlers onto the mux by TYPE ASSERTION for the capabilities that
// are deliberately absent from coreapi.Client (maintenance.Cleaner, and the
// deliverability breaker). A type assertion is invisible to unit tests: every fake
// in the repo satisfies the narrow interface, so a wiring regression here — a
// renamed method, a value/pointer receiver change, a handler removed from Register
// — would leave the whole unit suite green and the breaker silently dead in
// production.
//
// This test therefore builds the REAL in-process client, runs the REAL Register,
// and dispatches a REAL task through the mux against real Postgres. asynq's
// ServeMux is itself an asynq.Handler, so ProcessTask exercises registration and
// routing without needing Redis.
func TestRegisterWiresTheDeliverabilityBreaker(t *testing.T) {
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

	kp, err := crypto.NewLocalKeyProvider(registerMasterKey)
	if err != nil {
		t.Fatalf("key provider: %v", err)
	}
	legacy, err := crypto.NewSealer(registerMasterKey)
	if err != nil {
		t.Fatalf("legacy sealer: %v", err)
	}
	secret := []byte("0123456789abcdef0123456789abcdef")
	core := inprocess.New(pool, crypto.NewKeyring(kp, keys.NewPgDEKStore(q), legacy),
		secret, "https://app.test", mail.GoogleOAuth{}, mail.MicrosoftOAuth{},
		secret, warmup.NewStaticLibrary())

	// A campaign that has breached: 10% bounce over the minimum sample.
	ws, campaignID := seedBreachingCampaign(t, ctx, pool, q)

	// The real registration. If the breaker's type assertion stops matching, no
	// handler is registered and ProcessTask returns "handler not found".
	mux := queue.NewMux()
	Register(mux, core, &mail.MultiSender{}, nil, nil, nil, nil, "https://app.test", secret, secret)

	payload, err := json.Marshal(queue.DeliverabilityEvaluatePayload{
		CampaignID: campaignID.String(), WorkspaceID: ws.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mux.ProcessTask(ctx, asynq.NewTask(queue.TaskDeliverabilityEvaluate, payload)); err != nil {
		t.Fatalf("dispatching %s through the real mux: %v", queue.TaskDeliverabilityEvaluate, err)
	}

	// The whole chain ran: mux routing -> handler -> the narrow Breaker interface ->
	// coreapi inprocess -> app/deliverability -> Postgres.
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM campaigns WHERE id = $1 AND workspace_id = $2`, campaignID, ws).Scan(&status); err != nil {
		t.Fatalf("status: %v", err)
	}
	if status != "paused" {
		t.Errorf("campaign status = %q after the evaluate task, want paused", status)
	}
	var events int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_pause_events WHERE workspace_id = $1 AND campaign_id = $2`,
		ws, campaignID).Scan(&events); err != nil {
		t.Fatalf("pause events: %v", err)
	}
	if events != 1 {
		t.Errorf("%d pause events, want 1", events)
	}

	// A second dispatch is a no-op, which is what makes the asynq retry safe.
	if err := mux.ProcessTask(ctx, asynq.NewTask(queue.TaskDeliverabilityEvaluate, payload)); err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM campaign_pause_events WHERE workspace_id = $1 AND campaign_id = $2`,
		ws, campaignID).Scan(&events); err != nil {
		t.Fatalf("pause events: %v", err)
	}
	if events != 1 {
		t.Errorf("%d pause events after a retried task, want 1", events)
	}
}

// seedBreachingCampaign builds a RUNNING campaign whose bounce rate is over the
// default threshold on a large enough sample for the breaker to be allowed to act:
// MinDelivered sends, a fifth of them bounced (20% against the 8% default).
func seedBreachingCampaign(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *gen.Queries) (uuid.UUID, uuid.UUID) {
	t.Helper()
	w, err := q.CreateWorkspace(ctx, "Register IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	email := uuid.NewString()[:8] + "@sender.test"
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: email, DisplayName: "IT",
		SmtpHost: "smtp.example.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.example.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 500, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: w.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: w.ID, Name: "C", MailboxID: mb.ID, ListID: lst.ID,
		Subject: "Hi", BodyText: "b",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE campaigns SET status = 'running' WHERE id = $1`, cam.ID); err != nil {
		t.Fatalf("running: %v", err)
	}

	now := time.Now()
	for i := range deliverability.MinDelivered {
		ct, err := q.UpsertContact(ctx, gen.UpsertContactParams{
			WorkspaceID: w.ID, Email: uuid.NewString() + "@recipient.test", FirstName: "C",
		})
		if err != nil {
			t.Fatalf("contact %d: %v", i, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, sent_at)
			 VALUES ($1,$2,$3,$4,'x@y.test','sent',$5)`,
			w.ID, cam.ID, ct.ID, mb.ID, now); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		if i%5 == 0 {
			if _, err := pool.Exec(ctx,
				`INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, status, stop_reason, stopped_at)
				 VALUES ($1,$2,$3,'stopped','bounced',$4)`,
				w.ID, cam.ID, ct.ID, now); err != nil {
				t.Fatalf("bounce %d: %v", i, err)
			}
		}
	}
	return w.ID, cam.ID
}
