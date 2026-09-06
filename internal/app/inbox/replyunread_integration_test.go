//go:build integration

package inbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// A landed reply marks its thread read.
//
// This is the bug these tests exist for, and it was live on the path the product
// actually uses. POST /inbox/threads/{id}/reply marks read at enqueue, but the
// composer does not use it — it sends through /schedule-reply for both Send and
// Send-later, because a zero undo window is an immediate send. That path
// deliberately does not mark read at enqueue (an undone send must not leave the
// thread read) and promised, in a comment, that unread "moves to the moment the
// send actually lands". Nothing implemented it, and BumpInboxThreadLastMessageAt
// explicitly did not touch unread on the stale grounds that Reply had already
// done so. So every thread replied to through the UI stayed unread forever.
//
// Asserted through the real write path against real Postgres: the rule lives in
// one SQL statement, and a fake store would only re-state it.
func TestALandedReplyMarksTheThreadRead(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	inboundAt := time.Now().UTC().Add(-time.Hour)
	threadID := f.seedThreadWithMessage(t, ctx, "inbound", inboundAt)
	if !f.unread(t, ctx, threadID) {
		t.Fatal("an inbound message left the thread read; the fixture cannot show a reply clearing it")
	}

	f.appendOutbound(t, ctx, threadID, inboundAt.Add(time.Minute))

	if f.unread(t, ctx, threadID) {
		t.Error("the thread is still unread after the reply landed — the state every thread " +
			"replied to through the composer was stuck in")
	}
}

// The guard, and the reason the rule is "the newest message is outbound" rather
// than an unconditional clear.
//
// An operator writes a reply, and while it waits out the undo window a new message
// arrives. Clearing unread when the reply finally lands would bury a message
// nobody has read — and the reply is not an answer to it, because it was written
// before it arrived.
func TestAReplyLandingAfterFreshInboundLeavesTheThreadUnread(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	base := time.Now().UTC().Add(-time.Hour)
	threadID := f.seedThreadWithMessage(t, ctx, "inbound", base)

	// Order matters, and getting it wrong makes this test prove nothing. The
	// fresh message must arrive BEFORE the reply is recorded, because recording an
	// inbound message sets unread itself — do it last and the flag is true no
	// matter what the reply did, which is exactly how the first draft of this test
	// passed against an unconditional clear.
	//
	// So: the new mail lands at +2min, and the reply the operator wrote at +1min
	// is recorded afterwards. Its occurred_at is older than the inbound, so the
	// newest message in the thread is inbound and the flag must survive.
	f.appendInbound(t, ctx, threadID, base.Add(2*time.Minute))
	f.appendOutbound(t, ctx, threadID, base.Add(time.Minute))

	if !f.unread(t, ctx, threadID) {
		t.Error("the thread was marked read even though the newest message is inbound; a reply " +
			"that lands after fresh mail must not bury it")
	}
}

// Belt and braces on the tenancy pin: the clear is part of a workspace-pinned
// UPDATE, so a foreign thread id must change nothing rather than clearing
// someone else's unread flag.
func TestAReplyNeverClearsUnreadInAnotherWorkspace(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	foreignWS, foreignMailbox := seedTenant(t, ctx, f.q)
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: foreignWS, MailboxID: foreignMailbox,
		RootMessageID: "<foreign-" + uuid.NewString() + "@sender.test>",
		Subject:       "theirs", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", MessageID: "<fm-" + uuid.NewString() + "@sender.test>",
		FromEmail: "lead@x.test", BodyText: "hi", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed foreign thread: %v", err)
	}

	// Our workspace tries to record a reply against their thread.
	if err := f.store.RecordOutboundReply(ctx, th.ID, f.ws, inbox.InsertMessageInput{
		Direction: "outbound", MessageID: "<x-" + uuid.NewString() + "@sender.test>",
		FromEmail: "me@sender.test", BodyText: "not mine", OccurredAt: time.Now().UTC(),
	}); err == nil {
		// The insert is expected to fail on the NOT NULL mailbox_id; if the
		// schema ever permits it, the unread flag must still be untouched.
		t.Log("cross-workspace RecordOutboundReply did not error; asserting the flag is intact")
	}

	var unread bool
	if err := f.pool.QueryRow(ctx,
		`SELECT unread FROM inbox_threads WHERE id = $1 AND workspace_id = $2`,
		th.ID, foreignWS).Scan(&unread); err != nil {
		t.Fatalf("read foreign thread: %v", err)
	}
	if !unread {
		t.Error("a reply recorded under another workspace cleared this thread's unread flag")
	}
}

func (f *fixture) unread(t *testing.T, ctx context.Context, threadID uuid.UUID) bool {
	t.Helper()
	var unread bool
	if err := f.pool.QueryRow(ctx,
		`SELECT unread FROM inbox_threads WHERE id = $1 AND workspace_id = $2`,
		threadID, f.ws).Scan(&unread); err != nil {
		t.Fatalf("read thread: %v", err)
	}
	return unread
}

// appendInbound adds a later inbound message to an existing thread through the
// real capture path, so unread is set the way a genuine arrival sets it.
func (f *fixture) appendInbound(t *testing.T, ctx context.Context, threadID uuid.UUID, occurredAt time.Time) {
	t.Helper()
	var root string
	if err := f.pool.QueryRow(ctx,
		`SELECT root_message_id FROM inbox_threads WHERE id = $1 AND workspace_id = $2`,
		threadID, f.ws).Scan(&root); err != nil {
		t.Fatalf("read thread root: %v", err)
	}
	if _, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox,
		RootMessageID: root, Subject: "cap", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", MessageID: "<in-" + uuid.NewString() + "@sender.test>",
		FromEmail: "lead@x.test", BodyText: "one more thing", OccurredAt: occurredAt,
	}); err != nil {
		t.Fatalf("append inbound: %v", err)
	}
}
