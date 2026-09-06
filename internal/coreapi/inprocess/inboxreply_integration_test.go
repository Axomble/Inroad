//go:build integration

package inprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These tests exercise GetInboxReplyJob/RecordInboxReply (the coreapi seam
// internal/worker/inbox's reply-send handler uses) against Postgres, reusing
// claimConnect/seedForClaim. Docker must be up.

// newInboxReplyClient builds the in-process coreapi.Client and asserts it
// implements the ReplyCore capability worker/inbox consumes it through, the
// same type-assertion pattern proven at the real composition root
// (internal/worker/inbox.Register).
type replyCore interface {
	GetInboxReplyJob(ctx context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error)
	RecordInboxReply(ctx context.Context, in coreapi.RecordInboxReplyInput) error
	ClaimInboxReply(ctx context.Context, workspaceID, taskID string) (bool, error)
	ReleaseInboxReply(ctx context.Context, workspaceID, taskID string) error
}

func newInboxReplyClient(t *testing.T, pool *pgxpool.Pool, q *gen.Queries) replyCore {
	t.Helper()
	core := New(pool, itKeyring(t, q), []byte("0123456789abcdef0123456789abcdef"),
		"https://app.test", mail.GoogleOAuth{}, mail.MicrosoftOAuth{},
		[]byte("warmup-secret-0123456789abcdef"), warmup.NewStaticLibrary())
	rc, ok := core.(replyCore)
	if !ok {
		t.Fatal("in-process coreapi.Client does not implement worker/inbox.ReplyCore's data-loading methods")
	}
	return rc
}

// seedThreadForReply creates a thread with ONE inbound message (via the SAME
// inbox.Service.RecordReply write path a real poll uses) and returns its id.
func seedThreadForReply(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, mailboxID uuid.UUID, subject string) uuid.UUID {
	t.Helper()
	inboxSvc := inbox.NewService(inbox.NewPgStore(pool))
	th, err := inboxSvc.RecordReply(ctx, inbox.RecordReplyInput{
		WorkspaceID: ws, MailboxID: mailboxID, RootMessageID: "<root-" + uuid.NewString() + "@sender.test>",
		Subject: subject, LastReplyClass: "neutral",
		Message: inbox.InsertMessageInput{
			Direction: "inbound", MessageID: "<inbound-" + uuid.NewString() + "@sender.test>",
			FromEmail: "lead@x.test", BodyText: "tell me more", OccurredAt: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return th.ID
}

// The core round trip: GetInboxReplyJob resolves the mailbox/subject/
// recipient/threading headers from the seeded inbound message, and
// RecordInboxReply then appends the outbound leg — reachable through
// inbox.Service.GetThread afterward, and counted by CountSentToday.
func TestGetInboxReplyJobAndRecordInboxReplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxReplyClient(t, pool, q)

	threadID := seedThreadForReply(t, ctx, pool, fx.ws, fx.mailboxID, "Question about pricing")

	job, err := client.GetInboxReplyJob(ctx, threadID.String(), fx.ws.String())
	if err != nil {
		t.Fatalf("GetInboxReplyJob: %v", err)
	}
	if job.MailboxID != fx.mailboxID.String() {
		t.Errorf("MailboxID = %q, want %q", job.MailboxID, fx.mailboxID.String())
	}
	if job.Subject != "Question about pricing" {
		t.Errorf("Subject = %q, want the thread's raw (unprefixed) subject", job.Subject)
	}
	if job.ToEmail != "lead@x.test" {
		t.Errorf("ToEmail = %q, want the latest inbound message's From: address", job.ToEmail)
	}
	if job.InReplyTo == "" || job.References == "" {
		t.Fatalf("job = %+v, want non-empty threading headers", job)
	}

	if err := client.RecordInboxReply(ctx, coreapi.RecordInboxReplyInput{
		WorkspaceID: fx.ws.String(), ThreadID: threadID.String(), MessageID: "<sent-" + uuid.NewString() + "@acme.test>",
		FromEmail: "from@acme.test", FromName: "Acme", ToEmail: job.ToEmail,
		Subject: "Re: " + job.Subject, BodyText: "here's our pricing",
	}); err != nil {
		t.Fatalf("RecordInboxReply: %v", err)
	}

	inboxSvc := inbox.NewService(inbox.NewPgStore(pool))
	detail, err := inboxSvc.GetThread(ctx, fx.ws, threadID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Messages) != 2 {
		t.Fatalf("messages = %+v, want 2 (the seeded inbound + the recorded outbound)", detail.Messages)
	}
	outbound := detail.Messages[len(detail.Messages)-1]
	if outbound.Direction != "outbound" || outbound.BodyText != "here's our pricing" {
		t.Errorf("outbound message = %+v, want the just-recorded reply", outbound)
	}
	// Recording the delivered reply clears unread, and this assertion used to say
	// the opposite. Its reasoning — "Service.Reply already did, before enqueuing,
	// on the API path this test bypasses" — was the bug written down: the composer
	// does not use that path, it schedules, and the scheduled path deliberately
	// does not mark read at enqueue. So nothing cleared it and every thread replied
	// to through the UI stayed unread. The newest message here is the outbound
	// reply, so the thread has been dealt with.
	if detail.Thread.Unread {
		t.Error("the thread is still unread after its reply was recorded; a landed reply marks " +
			"the thread read when it is the newest message")
	}

	// CountSentToday's extension (queries/send.sql): the manual reply just
	// recorded is an outbound inbox_messages row, not a `sends` row, but it
	// must still be counted toward the mailbox's daily volume.
	n, err := q.CountSentToday(ctx, fx.mailboxID)
	if err != nil {
		t.Fatalf("CountSentToday: %v", err)
	}
	if n != 1 {
		t.Fatalf("CountSentToday = %d, want 1 (the manual reply must count toward daily volume)", n)
	}
}

// recordingPendingEnqueuer stands in for the queue: the immediate reply path
// enqueues a POINTER to the row it just wrote, and this records the arguments
// without needing Redis. Its signature is the assertion — two ids and an
// instant, no body.
type recordingPendingEnqueuer struct {
	pendingIDs []string
	sendAfters []time.Time
}

func (r *recordingPendingEnqueuer) EnqueuePendingInboxReply(pendingID, _ string, sendAfter time.Time) error {
	r.pendingIDs = append(r.pendingIDs, pendingID)
	r.sendAfters = append(r.sendAfters, sendAfter)
	return nil
}

// appClockSkewAhead simulates the API server's clock running ahead of Postgres's.
// Deliberately smaller than inbox's immediateSendClaimSlack (30s) and larger than
// zero, so it is the ONE thing that decides this test.
const appClockSkewAhead = 5 * time.Second

// TestImmediateReplyRowIsClaimableAgainstTheRealSQLGuard is the clock-skew half
// of moving the reply body out of the task payload.
//
// POST /inbox/threads/{id}/reply now writes an inbox_pending_replies row with a
// send_after in the past and enqueues a pointer to it. ClaimInboxPendingReply
// guards on `send_after <= now()` evaluated on the DATABASE clock, while
// send_after is stamped from the APP clock. If the app clock is even slightly
// ahead, a send_after of "now" is in the database's future: the task fires, the
// guarded UPDATE matches nothing, ErrPendingNotClaimable is not retryable, and
// the row sits 'scheduled' forever — there is no sweeper. The operator is told
// their reply was sent and it never leaves.
//
// The Service's clock is therefore pinned AHEAD of the database's on purpose.
// Against the real statement, the row must still be claimable at once.
func TestImmediateReplyRowIsClaimableAgainstTheRealSQLGuard(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)

	store := inbox.NewPgStore(pool)
	enq := &recordingPendingEnqueuer{}
	svc := inbox.NewService(store,
		inbox.WithPendingReplyStore(store),
		inbox.WithPendingReplyEnqueuer(enq),
		inbox.WithClock(func() time.Time { return time.Now().Add(appClockSkewAhead) }),
	)

	threadID := seedThreadForReply(t, ctx, pool, fx.ws, fx.mailboxID, "Immediate reply")
	if err := svc.Reply(ctx, fx.ws, threadID, "sending this right now", nil); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	row, err := store.PendingReplyForThread(ctx, fx.ws, threadID)
	if err != nil {
		t.Fatalf("PendingReplyForThread: %v", err)
	}
	if row.BodyText != "sending this right now" {
		t.Errorf("row body = %q, want the reply text stored on the ROW, not in the task", row.BodyText)
	}
	if row.Status != inbox.PendingStatusScheduled {
		t.Fatalf("row status = %q, want %q", row.Status, inbox.PendingStatusScheduled)
	}
	if len(enq.pendingIDs) != 1 || enq.pendingIDs[0] != row.ID.String() {
		t.Fatalf("enqueued %v, want the single row id %s", enq.pendingIDs, row.ID)
	}

	// THE ASSERTION. The real ClaimInboxPendingReply statement, with its
	// `send_after <= now()` guard on Postgres's own clock, against a row stamped
	// by an app clock running ahead of it.
	if err := svc.ClaimPendingReply(ctx, fx.ws, row.ID); err != nil {
		t.Fatalf("ClaimPendingReply immediately after Reply: %v — the row a worker picks up the "+
			"instant its task fires must be claimable, or the reply is stranded 'scheduled' "+
			"forever with no sweeper to rescue it", err)
	}

	claimed, err := store.GetPendingReply(ctx, fx.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if claimed.Status != inbox.PendingStatusSending {
		t.Errorf("status after claim = %q, want %q", claimed.Status, inbox.PendingStatusSending)
	}
}

// scheduledButSoon is how far ahead the deferred reply below is scheduled.
//
// Deliberately INSIDE immediateSendClaimSlack (30s) rather than comfortably
// past it. The test's whole sentence is "the slack must not leak into a
// scheduled send", and an hour would satisfy it under the very implementation it
// is meant to reject: relax the SQL guard by an unconditional 30 seconds and a
// row due in an hour is still unclaimable, so the test would pass and prove
// nothing. Five seconds is claimable under that mistake and not under the real
// statement.
const scheduledButSoon = 5 * time.Second

// The slack must not leak into a genuinely SCHEDULED send. A reply the operator
// deferred is not claimable before its moment: the SQL guard is what stops a
// task that fires early from delivering ahead of time, and relaxing it — rather
// than backdating the already-due case — would have broken exactly that.
func TestScheduledReplyIsNotClaimableBeforeItsMoment(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)

	store := inbox.NewPgStore(pool)
	svc := inbox.NewService(store,
		inbox.WithPendingReplyStore(store),
		inbox.WithPendingReplyEnqueuer(&recordingPendingEnqueuer{}),
	)

	threadID := seedThreadForReply(t, ctx, pool, fx.ws, fx.mailboxID, "Later reply")
	at := time.Now().Add(scheduledButSoon)
	row, err := svc.ScheduleReply(ctx, fx.ws, threadID, "not yet", &at, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}

	if err := svc.ClaimPendingReply(ctx, fx.ws, row.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Fatalf("ClaimPendingReply on a reply due in %v = %v, want ErrPendingNotClaimable — a send "+
			"scheduled within the immediate-path slack must still wait for its own moment",
			scheduledButSoon, err)
	}
}

// GetInboxReplyJob on an unknown thread propagates GetThread's own not-found
// error rather than special-casing it — the API layer (Service.Reply) is
// what turns that into a 404 before ever enqueuing, so the worker seeing one
// at all would mean the thread vanished between enqueue and this task
// running; either way there is nothing to reply to.
func TestGetInboxReplyJobOnAnUnknownThreadPropagatesNotFound(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxReplyClient(t, pool, q)

	if _, err := client.GetInboxReplyJob(ctx, uuid.New().String(), fx.ws.String()); err == nil {
		t.Fatal("want an error for an unknown thread id, got nil")
	}
}

// GetInboxReplyJob is workspace-pinned: a foreign workspace cannot read
// another tenant's thread.
func TestGetInboxReplyJobIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxReplyClient(t, pool, q)

	threadID := seedThreadForReply(t, ctx, pool, fx.ws, fx.mailboxID, "Own workspace only")

	if _, err := client.GetInboxReplyJob(ctx, threadID.String(), fx.foreignWS.String()); err == nil {
		t.Fatal("want an error for a foreign workspace reading another tenant's thread, got nil")
	}
}

// ClaimInboxReply/ReleaseInboxReply (worker/inbox.ReplyCore's claim-before-
// send guard) against the REAL idempotency_keys table this reuses (migration
// 000045): a fresh claim succeeds, a second claim of the SAME (workspace,
// task id) fails (claimed=false, not an error) until released, and releasing
// lets a subsequent claim of the identical key succeed again — exactly the
// insert/delete pair the claim reuses.
func TestClaimInboxReplyRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxReplyClient(t, pool, q)
	taskID := "inboxreply:" + uuid.NewString() + ":1700000000"

	claimed, err := client.ClaimInboxReply(ctx, fx.ws.String(), taskID)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !claimed {
		t.Fatal("first claim of a fresh task id must succeed")
	}

	claimed, err = client.ClaimInboxReply(ctx, fx.ws.String(), taskID)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("a second claim of the SAME task id must fail while the first is still held")
	}

	if err := client.ReleaseInboxReply(ctx, fx.ws.String(), taskID); err != nil {
		t.Fatalf("release: %v", err)
	}

	claimed, err = client.ClaimInboxReply(ctx, fx.ws.String(), taskID)
	if err != nil {
		t.Fatalf("re-claim after release: %v", err)
	}
	if !claimed {
		t.Fatal("a claim after release must succeed again — the retry's own re-claim")
	}
}

// A workspace can never see, let alone conflict with, another workspace's
// claim on the identical raw task id — the (workspace_id, key) primary key
// is what makes this hold, exactly as it does for the generic HTTP
// idempotency cache this reuses.
func TestClaimInboxReplyIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxReplyClient(t, pool, q)
	taskID := "inboxreply:" + uuid.NewString() + ":1700000000"

	claimed, err := client.ClaimInboxReply(ctx, fx.ws.String(), taskID)
	if err != nil || !claimed {
		t.Fatalf("claim in ws: claimed=%v err=%v, want true/nil", claimed, err)
	}

	claimed, err = client.ClaimInboxReply(ctx, fx.foreignWS.String(), taskID)
	if err != nil {
		t.Fatalf("claim in foreign ws: %v", err)
	}
	if !claimed {
		t.Fatal("a foreign workspace's claim on the identical raw task id must succeed independently")
	}
}
