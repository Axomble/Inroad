//go:build integration

package sequence

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

// itSender records every message it "sends" and returns a per-call Message-ID
// so threading (In-Reply-To referencing the prior step) can be asserted.
type itSender struct {
	sent []mail.Message
	n    int
}

func (s *itSender) Send(_ mail.SMTPConfig, m mail.Message) (string, error) {
	s.sent = append(s.sent, m)
	s.n++
	return "<msg-" + string(rune('a'+s.n)) + "@inroad>", nil
}

// itEnq captures scheduling instead of hitting Redis, so the test drives each
// advance manually and asserts the lazy chain's intent.
type itEnq struct {
	at map[string]time.Time
	in map[string]time.Duration
}

func newITEnq() *itEnq {
	return &itEnq{at: map[string]time.Time{}, in: map[string]time.Duration{}}
}
func (e *itEnq) EnqueueAdvanceAt(id, _ string, t time.Time) error     { e.at[id] = t; return nil }
func (e *itEnq) EnqueueAdvanceIn(id, _ string, d time.Duration) error { e.in[id] = d; return nil }

type itFixture struct {
	q          *gen.Queries
	core       coreapi.Client
	ws         uuid.UUID
	campaignID uuid.UUID
	contactID  uuid.UUID
	email      string
}

// seedCampaign builds a workspace + mailbox + list + one contact + campaign
// with the given step (subject, body, delaySeconds) list, and enrolls the
// contact. Returns the enrollment id. The pool backs the coreapi client (its
// MarkStepSent opens a transaction), while q is used for direct seeding.
func seedCampaign(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *gen.Queries, sealer *crypto.Sealer, steps [][3]string) itFixture {
	t.Helper()
	ct, err := sealer.Seal([]byte("smtp-app-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ws, err := q.CreateWorkspace(ctx, "Seq IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws.ID, Provider: "smtp", Email: "from@acme.test", DisplayName: "Acme",
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: "from@acme.test",
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: "from@acme.test",
		SecretCiphertext: ct, UseTls: true, DailyCap: 500, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	email := "alice-" + uuid.NewString() + "@x.test"
	c, err := q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: ws.ID, Email: email, FirstName: "Alice"})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: lst.ID, ContactID: c.ID}); err != nil {
		t.Fatalf("member: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws.ID, Name: "Camp", MailboxID: mb.ID, ListID: lst.ID,
		Subject: steps[0][0], BodyText: steps[0][1],
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	for i, s := range steps {
		delay := int32(0)
		if s[2] != "" {
			delay = 0 // tests use 0-delay steps for speed; value carried only for documentation
		}
		if _, err := q.CreateStep(ctx, gen.CreateStepParams{
			WorkspaceID: ws.ID, CampaignID: cam.ID, StepOrder: int32(i + 1),
			DelaySeconds: delay, Subject: s[0], BodyText: s[1],
		}); err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
	}
	sealerKey := []byte("0123456789abcdef0123456789abcdef")
	return itFixture{
		q: q, core: inprocess.New(pool, sealer, sealerKey, "https://app.test"),
		ws: ws.ID, campaignID: cam.ID, contactID: c.ID, email: email,
	}
}

func advance(t *testing.T, core coreapi.Client, s Sender, enq Enqueuer, enrollmentID, ws string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"enrollment_id": enrollmentID, "workspace_id": ws})
	h := AdvanceHandler(core, s, enq, "https://app.test", []byte("0123456789abcdef0123456789abcdef"))
	if err := h(context.Background(), asynq.NewTask("sequence:advance", b)); err != nil {
		t.Fatalf("advance: %v", err)
	}
}

func newSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	s, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	return s
}

func connect(t *testing.T) (*pgxpool.Pool, *gen.Queries, func()) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return pool, gen.New(pool), pool.Close
}

// TestSequenceMultiStepThreaded drives a 2-step sequence end-to-end against
// real Postgres: enroll → advance (step 1) → advance (step 2). Asserts
// personalization, that step 2 threads onto step 1 (In-Reply-To set + "Re:"
// subject), and that the enrollment completes.
func TestSequenceMultiStepThreaded(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	// Step 2 leaves its subject empty: per spec A5 that means "reply in thread"
	// → the send subject becomes "Re: <step-1 subject>". (A non-empty step-2
	// subject would be a deliberate new subject used verbatim, still threaded.)
	fx := seedCampaign(t, ctx, pool, q, sealer, [][3]string{
		{"Hi {{first_name}}", "Hello {{first_name}}", "0"},
		{"", "Just checking in", "0"},
	})
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v ids=%d", err, len(ids))
	}
	eid := ids[0].ID.String()
	snd, enq := &itSender{}, newITEnq()

	// Step 1.
	advance(t, fx.core, snd, enq, eid, fx.ws.String())
	if len(snd.sent) != 1 || snd.sent[0].Subject != "Hi Alice" {
		t.Fatalf("step 1 send wrong: %+v", snd.sent)
	}
	if snd.sent[0].InReplyTo != "" {
		t.Fatalf("step 1 must not thread, got In-Reply-To %q", snd.sent[0].InReplyTo)
	}
	if _, ok := enq.at[eid]; !ok {
		t.Fatal("step 1 should schedule step 2 (lazy chain)")
	}

	// Step 2.
	advance(t, fx.core, snd, enq, eid, fx.ws.String())
	if len(snd.sent) != 2 {
		t.Fatalf("expected 2 sends, got %d", len(snd.sent))
	}
	// Empty step-2 subject ⇒ "Re: <step-1 subject>" (step-1 subject personalized:
	// "Hi {{first_name}}" → "Hi Alice").
	if snd.sent[1].Subject != "Re: Hi Alice" {
		t.Fatalf("step 2 subject should be a threaded reply, got %q", snd.sent[1].Subject)
	}
	if snd.sent[1].InReplyTo == "" {
		t.Fatalf("step 2 must thread onto step 1 (In-Reply-To set)")
	}

	// Enrollment completed at step 2.
	e, err := q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: ids[0].ID, WorkspaceID: fx.ws})
	if err != nil {
		t.Fatalf("get enrollment: %v", err)
	}
	if e.Status != "completed" || e.CurrentStep != 2 {
		t.Fatalf("enrollment not completed: status=%s step=%d", e.Status, e.CurrentStep)
	}
}

// TestSequenceBackwardCompatSingleStep proves a one-step campaign (the old
// single-message shape) enrolls and completes after a single advance.
func TestSequenceBackwardCompatSingleStep(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	fx := seedCampaign(t, ctx, pool, q, sealer, [][3]string{{"Solo {{first_name}}", "One and done", "0"}})
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v", err)
	}
	snd, enq := &itSender{}, newITEnq()
	advance(t, fx.core, snd, enq, ids[0].ID.String(), fx.ws.String())

	if len(snd.sent) != 1 || snd.sent[0].Subject != "Solo Alice" {
		t.Fatalf("single-step send wrong: %+v", snd.sent)
	}
	if _, ok := enq.at[ids[0].ID.String()]; ok {
		t.Fatal("single-step campaign must not schedule a next step")
	}
	e, _ := q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: ids[0].ID, WorkspaceID: fx.ws})
	if e.Status != "completed" {
		t.Fatalf("expected completed, got %s", e.Status)
	}
}

// TestSequenceCapDeferralsResetOnSend proves cap_deferrals is a CONSECUTIVE
// counter, reset to 0 whenever a step successfully sends (AdvanceEnrollmentStep
// / CompleteEnrollment), not a lifetime total. A long healthy campaign that
// occasionally brushes the daily cap between sends must never accumulate past
// maxCapDeferrals and be wrongly failed.
func TestSequenceCapDeferralsResetOnSend(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	fx := seedCampaign(t, ctx, pool, q, sealer, [][3]string{
		{"Hi {{first_name}}", "Hello", "0"},
		{"Follow up", "Ping", "0"},
	})
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v", err)
	}
	eid := ids[0].ID

	// Simulate a run of cap-defers before the send lands.
	for i := 0; i < 5; i++ {
		if _, err := q.IncrementEnrollmentCapDeferrals(ctx, gen.IncrementEnrollmentCapDeferralsParams{ID: eid, WorkspaceID: fx.ws}); err != nil {
			t.Fatalf("increment cap_deferrals: %v", err)
		}
	}
	e, _ := q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: eid, WorkspaceID: fx.ws})
	if e.CapDeferrals != 5 {
		t.Fatalf("precondition: want cap_deferrals=5, got %d", e.CapDeferrals)
	}

	// Step 1 sends → AdvanceEnrollmentStep must reset the counter.
	snd, enq := &itSender{}, newITEnq()
	advance(t, fx.core, snd, enq, eid.String(), fx.ws.String())
	e, _ = q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: eid, WorkspaceID: fx.ws})
	if e.CapDeferrals != 0 {
		t.Fatalf("cap_deferrals must reset to 0 on a successful send, got %d", e.CapDeferrals)
	}

	// A fresh defer after the reset starts the count over (consecutive, not
	// lifetime): the next bump is 1, not 6.
	n, err := q.IncrementEnrollmentCapDeferrals(ctx, gen.IncrementEnrollmentCapDeferralsParams{ID: eid, WorkspaceID: fx.ws})
	if err != nil {
		t.Fatalf("increment after reset: %v", err)
	}
	if n != 1 {
		t.Fatalf("post-reset counter must be consecutive (want 1), got %d", n)
	}
}

// TestSequenceStopOnSuppression proves a suppressed contact is stopped (not
// emailed) on advance.
func TestSequenceStopOnSuppression(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	fx := seedCampaign(t, ctx, pool, q, sealer, [][3]string{
		{"Hi {{first_name}}", "Hello", "0"},
		{"Follow up", "Ping", "0"},
	})
	if err := q.AddSuppression(ctx, gen.AddSuppressionParams{WorkspaceID: fx.ws, Email: fx.email, Reason: "unsubscribe"}); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v", err)
	}
	snd, enq := &itSender{}, newITEnq()
	advance(t, fx.core, snd, enq, ids[0].ID.String(), fx.ws.String())

	if len(snd.sent) != 0 {
		t.Fatalf("suppressed contact must not be emailed, sent %d", len(snd.sent))
	}
	e, _ := q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: ids[0].ID, WorkspaceID: fx.ws})
	if e.Status != "stopped" || e.StopReason == nil || *e.StopReason != "suppressed" {
		t.Fatalf("expected stopped/suppressed, got status=%s reason=%v", e.Status, e.StopReason)
	}
}
