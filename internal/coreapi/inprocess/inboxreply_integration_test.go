//go:build integration

package inprocess

import (
	"context"
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
	// RecordInboxReply itself never flips unread (Service.Reply already did,
	// before enqueuing, on the API path this test bypasses) — the thread
	// stays exactly as RecordReply left it (unread=true on first insert).
	if !detail.Thread.Unread {
		t.Error("RecordInboxReply must not have touched unread")
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
