//go:build integration

package inbox

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// This file mirrors internal/worker/sequence/sequence_integration_test.go's
// harness (build tag, DB connect helper, seeding shape) but drives
// PollHandler with a fake mail.InboxReader against the REAL inprocess
// coreapi + dockerized Postgres — the point is to exercise the coreapi/DB
// path end-to-end; the IMAP fetch itself is unit-covered in package mail.

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
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

func newSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	s, err := crypto.NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	return s
}

// itFixture bundles the seeded IDs a poll-integration test needs: a
// two-step campaign with one contact enrolled and step 1 already "sent"
// with a known Message-ID, leaving the enrollment active so a subsequent
// reply/bounce has something to stop.
type itFixture struct {
	q            *gen.Queries
	core         coreapi.Client
	ws           uuid.UUID
	mailboxID    uuid.UUID
	campaignID   uuid.UUID
	enrollmentID uuid.UUID
	email        string
}

// seedActiveEnrollment builds a workspace + mailbox + list + one contact +
// two-step campaign, enrolls the contact, then drives the real coreapi
// GetStepSendJob/MarkStepSent path to record step 1's send under
// firstStepMessageID. Because the campaign has two steps, MarkStepSent
// leaves the enrollment 'active' (not completed) — exactly the shape a
// reply/bounce arriving before step 2 needs to stop.
func seedActiveEnrollment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *gen.Queries, sealer *crypto.Sealer, firstStepMessageID string) itFixture {
	t.Helper()
	ct, err := sealer.Seal([]byte("imap-app-password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ws, err := q.CreateWorkspace(ctx, "Inbox IT "+uuid.NewString())
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
		Subject: "Hi {{first_name}}", BodyText: "Hello",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if _, err := q.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: ws.ID, CampaignID: cam.ID, StepOrder: 1, DelaySeconds: 0,
		Subject: "Hi {{first_name}}", BodyText: "Hello",
	}); err != nil {
		t.Fatalf("step 1: %v", err)
	}
	if _, err := q.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: ws.ID, CampaignID: cam.ID, StepOrder: 2, DelaySeconds: 3600,
		Subject: "", BodyText: "Just checking in",
	}); err != nil {
		t.Fatalf("step 2: %v", err)
	}

	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: cam.ID, WorkspaceID: ws.ID})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v ids=%d", err, len(ids))
	}
	eid := ids[0].ID

	sealerKey := []byte("0123456789abcdef0123456789abcdef")
	core := inprocess.New(pool, sealer, sealerKey, "https://app.test", mail.GoogleOAuth{})

	job, err := core.GetStepSendJob(ctx, eid.String(), ws.ID.String())
	if err != nil {
		t.Fatalf("get step send job: %v", err)
	}
	if job.Skip {
		t.Fatal("step 1 send job unexpectedly skipped")
	}
	if _, err := core.MarkStepSent(ctx, job, coreapi.StepResult{Status: "sent", MessageID: firstStepMessageID}); err != nil {
		t.Fatalf("mark step sent: %v", err)
	}

	return itFixture{
		q: q, core: core, ws: ws.ID, mailboxID: mb.ID,
		campaignID: cam.ID, enrollmentID: eid, email: email,
	}
}

func pollTaskFor(t *testing.T, mailboxID, workspaceID string) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.InboxPollPayload{MailboxID: mailboxID, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	return asynq.NewTask(queue.TaskInboxPoll, b)
}

func getEnrollment(t *testing.T, ctx context.Context, q *gen.Queries, ws, id uuid.UUID) gen.SequenceEnrollment {
	t.Helper()
	e, err := q.GetEnrollment(ctx, gen.GetEnrollmentParams{ID: id, WorkspaceID: ws})
	if err != nil {
		t.Fatalf("get enrollment: %v", err)
	}
	return e
}

func getMailbox(t *testing.T, ctx context.Context, q *gen.Queries, ws, id uuid.UUID) gen.Mailbox {
	t.Helper()
	m, err := q.GetMailbox(ctx, gen.GetMailboxParams{ID: id, WorkspaceID: ws})
	if err != nil {
		t.Fatalf("get mailbox: %v", err)
	}
	return m
}

// TestInboxIntegrationReplyStopsEnrollment: an inbound reply whose References
// header carries step 1's Message-ID stops the enrollment 'replied' and
// advances the mailbox's poll cursor.
func TestInboxIntegrationReplyStopsEnrollment(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	msgID := "<step1-" + uuid.NewString() + "@test>"
	fx := seedActiveEnrollment(t, ctx, pool, q, sealer, msgID)
	if err := fx.core.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 10, 5); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	raw := "From: alice@example.com\nTo: bob@example.com\nSubject: Re: Hi\nReferences: " +
		msgID + "\n\nSounds good.\n"
	reader := &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{inboundMsg(t, 11, raw)}}

	if err := PollHandler(fx.core, reader)(ctx, pollTaskFor(t, fx.mailboxID.String(), fx.ws.String())); err != nil {
		t.Fatalf("poll: %v", err)
	}

	e := getEnrollment(t, ctx, q, fx.ws, fx.enrollmentID)
	if e.Status != "stopped" || e.StopReason == nil || *e.StopReason != "replied" {
		t.Fatalf("expected stopped/replied, got status=%s reason=%v", e.Status, e.StopReason)
	}
	mb := getMailbox(t, ctx, q, fx.ws, fx.mailboxID)
	if mb.InboxLastSeenUid != 11 || mb.InboxUidValidity != 5 {
		t.Fatalf("expected cursor advanced to (11, 5), got (%d, %d)", mb.InboxLastSeenUid, mb.InboxUidValidity)
	}
}

// TestInboxIntegrationHardBounceStopsAndSuppresses: a hard DSN referencing
// step 1's Message-ID stops the enrollment 'bounced' AND suppresses the
// contact's address.
func TestInboxIntegrationHardBounceStopsAndSuppresses(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	// hardBounceDSN's embedded original Message-ID is "<orig@x>" (dsn_test.go).
	fx := seedActiveEnrollment(t, ctx, pool, q, sealer, "<orig@x>")
	if err := fx.core.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 10, 5); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	reader := &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{inboundMsg(t, 11, hardBounceDSN)}}
	if err := PollHandler(fx.core, reader)(ctx, pollTaskFor(t, fx.mailboxID.String(), fx.ws.String())); err != nil {
		t.Fatalf("poll: %v", err)
	}

	e := getEnrollment(t, ctx, q, fx.ws, fx.enrollmentID)
	if e.Status != "stopped" || e.StopReason == nil || *e.StopReason != "bounced" {
		t.Fatalf("expected stopped/bounced, got status=%s reason=%v", e.Status, e.StopReason)
	}
	suppressed, err := q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: fx.ws, Lower: fx.email})
	if err != nil {
		t.Fatalf("is suppressed: %v", err)
	}
	if !suppressed {
		t.Fatalf("expected %s to be suppressed after a hard bounce", fx.email)
	}
}

// TestInboxIntegrationSoftBounceNoOp: a soft DSN must not change the
// enrollment or suppress anything, even though the cursor still advances
// past the consumed message.
func TestInboxIntegrationSoftBounceNoOp(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	// softBounceDSN's embedded original Message-ID is "<orig2@y>" (dsn_test.go).
	fx := seedActiveEnrollment(t, ctx, pool, q, sealer, "<orig2@y>")
	if err := fx.core.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 10, 5); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	reader := &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{inboundMsg(t, 11, softBounceDSN)}}
	if err := PollHandler(fx.core, reader)(ctx, pollTaskFor(t, fx.mailboxID.String(), fx.ws.String())); err != nil {
		t.Fatalf("poll: %v", err)
	}

	e := getEnrollment(t, ctx, q, fx.ws, fx.enrollmentID)
	if e.Status != "active" || e.StopReason != nil {
		t.Fatalf("a soft bounce must not stop the enrollment, got status=%s reason=%v", e.Status, e.StopReason)
	}
	suppressed, err := q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: fx.ws, Lower: fx.email})
	if err != nil {
		t.Fatalf("is suppressed: %v", err)
	}
	if suppressed {
		t.Fatal("a soft bounce must not suppress the contact")
	}
	mb := getMailbox(t, ctx, q, fx.ws, fx.mailboxID)
	if mb.InboxLastSeenUid != 11 {
		t.Fatalf("cursor must still advance past a soft bounce, got %d", mb.InboxLastSeenUid)
	}
}

// TestInboxIntegrationFirstPollRebaselinesCursor: a never-polled mailbox
// (UIDVALIDITY==0) re-baselines its cursor to uidNext-1 on the first poll
// pass and fetches/processes nothing, leaving the enrollment untouched.
func TestInboxIntegrationFirstPollRebaselinesCursor(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	fx := seedActiveEnrollment(t, ctx, pool, q, sealer, "<unused@test>")
	// No SetInboxCursor call: the freshly created mailbox row keeps its
	// DB-default UIDVALIDITY of 0 — a never-polled mailbox.

	reader := &fakeReader{uidValidity: 7, uidNext: 51}
	if err := PollHandler(fx.core, reader)(ctx, pollTaskFor(t, fx.mailboxID.String(), fx.ws.String())); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if reader.fetchCalled {
		t.Fatal("first poll must not fetch any messages")
	}

	mb := getMailbox(t, ctx, q, fx.ws, fx.mailboxID)
	if mb.InboxLastSeenUid != 50 || mb.InboxUidValidity != 7 {
		t.Fatalf("expected cursor re-baselined to (50, 7), got (%d, %d)", mb.InboxLastSeenUid, mb.InboxUidValidity)
	}
	e := getEnrollment(t, ctx, q, fx.ws, fx.enrollmentID)
	if e.Status != "active" || e.StopReason != nil {
		t.Fatalf("a re-baseline pass must not touch the enrollment, got status=%s reason=%v", e.Status, e.StopReason)
	}
}

// TestInboxIntegrationRepollIsIdempotent: re-running the poll after a reply
// has already stopped the enrollment must be a no-op — Store.Stop is a
// no-op on an already-inactive enrollment (see enrollment.Store.Stop) — so
// the cursor and enrollment state are unchanged by the repeat pass.
func TestInboxIntegrationRepollIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	sealer := newSealer(t)

	msgID := "<step1-" + uuid.NewString() + "@test>"
	fx := seedActiveEnrollment(t, ctx, pool, q, sealer, msgID)
	if err := fx.core.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 10, 5); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	raw := "From: alice@example.com\nTo: bob@example.com\nSubject: Re: Hi\nReferences: " +
		msgID + "\n\nSounds good.\n"
	reader := &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{inboundMsg(t, 11, raw)}}

	task := pollTaskFor(t, fx.mailboxID.String(), fx.ws.String())
	if err := PollHandler(fx.core, reader)(ctx, task); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	e := getEnrollment(t, ctx, q, fx.ws, fx.enrollmentID)
	if e.Status != "stopped" || e.StopReason == nil || *e.StopReason != "replied" {
		t.Fatalf("first poll: expected stopped/replied, got status=%s reason=%v", e.Status, e.StopReason)
	}

	// Re-poll with the same reader (simulating the message still being
	// present) must not error and must not change anything further.
	if err := PollHandler(fx.core, reader)(ctx, task); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	e2 := getEnrollment(t, ctx, q, fx.ws, fx.enrollmentID)
	if e2.Status != "stopped" || e2.StopReason == nil || *e2.StopReason != "replied" {
		t.Fatalf("re-poll must be idempotent, got status=%s reason=%v", e2.Status, e2.StopReason)
	}
	mb := getMailbox(t, ctx, q, fx.ws, fx.mailboxID)
	if mb.InboxLastSeenUid != 11 || mb.InboxUidValidity != 5 {
		t.Fatalf("re-poll must leave the cursor unchanged at (11, 5), got (%d, %d)", mb.InboxLastSeenUid, mb.InboxUidValidity)
	}
}
