//go:build integration

package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/queue"
	workerinbox "github.com/inroad/inroad/internal/worker/inbox"
)

// Slice 3b of the coreapi remote transport, end to end: the MANUAL REPLY AND
// COMPOSE protocol, against real Postgres, through the real handler, from a
// worker-side client that HAS NO DATABASE — and, for the tests this slice exists
// for, with the response DROPPED after the control plane committed.
//
// # Why this file drives the real send handlers
//
// The question a write raises is not "does the answer cross" but "when the answer
// is LOST, what does the retry do" — and the retry is
// internal/worker/inbox.PendingReplySendHandler re-running from the top, through
// the claim, the suppression re-check, the transport resolve and the send. So the
// handler is what runs here, with a counting Sender standing in for SMTP.
//
// asynq is deliberately absent, exactly as in remoteoutcomes_integration_test.go:
// its retry IS a re-invocation of the same handler with the same payload, and
// calling the handler twice is that without needing a Redis.
//
// The fault injector is slice 3's (responseDropper, in that file) rather than a
// third one. Its `drop` runs the REAL handler to completion against a throwaway
// recorder — so everything it committed is committed — and then hijacks the
// connection and closes it without writing a byte.

// seedPendingReply writes one scheduled reply on a fresh thread and returns the
// row id, through the SAME inbox.Service the API uses, so the row's shape is the
// product's rather than this test's.
func seedPendingReply(t *testing.T, ctx context.Context, f poolFixture, body string) uuid.UUID {
	t.Helper()
	store := inbox.NewPgStore(f.pool)
	svc := inbox.NewService(store,
		inbox.WithPendingReplyStore(store),
		inbox.WithPendingReplyEnqueuer(&recordingPendingEnqueuer{}),
	)
	threadID := seedThreadForReply(t, ctx, f.pool, f.ws, f.mailboxA, "Question about pricing")
	if err := svc.Reply(ctx, f.ws, threadID, body, nil); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	row, err := store.PendingReplyForThread(ctx, f.ws, threadID)
	if err != nil {
		t.Fatalf("PendingReplyForThread: %v", err)
	}
	return row.ID
}

// seedPendingCompose writes one composed email that is already due.
func seedPendingCompose(t *testing.T, ctx context.Context, f poolFixture, body string) uuid.UUID {
	t.Helper()
	store := inbox.NewPgStore(f.pool)
	svc := inbox.NewService(store,
		inbox.WithComposeStore(store),
		inbox.WithPendingReplyStore(store),
	)
	row, err := svc.ScheduleCompose(ctx, f.ws, inbox.CreatePendingComposeInput{
		WorkspaceID: f.ws, MailboxID: f.mailboxA, ToEmails: []string{"lead@x.test"},
		Subject: "Intro", BodyText: body,
	}, nil)
	if err != nil {
		t.Fatalf("ScheduleCompose: %v", err)
	}
	// ScheduleCompose applies the workspace's undo window, so the row is not due
	// yet. The undo window is not what this file is testing.
	if _, err := f.pool.Exec(ctx,
		`UPDATE inbox_pending_composes SET send_after = now() - interval '1 minute'
		 WHERE id = $1 AND workspace_id = $2`, row.ID, f.ws); err != nil {
		t.Fatalf("make the compose due: %v", err)
	}
	return row.ID
}

// pendingReplyOnce runs the inbox:pending_reply_send handler exactly the way
// asynq delivers it: one task, one payload, one invocation. Calling it twice IS
// the retry.
func pendingReplyOnce(t *testing.T, ctx context.Context, core coreapi.Client, s *countingSender,
	pendingID, workspaceID string,
) error {
	t.Helper()
	pc, ok := core.(workerinbox.PendingReplyCore)
	if !ok {
		t.Fatalf("the remote-sourced client (%T) does not satisfy worker/inbox.PendingReplyCore — "+
			"these methods are reached by TYPE ASSERTION, so this is how a signature drift "+
			"silently unregisters the handler", core)
	}
	payload, err := json.Marshal(queue.InboxPendingReplySendPayload{PendingID: pendingID, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	h := workerinbox.PendingReplySendHandler(pc, s)
	return h(ctx, asynq.NewTask(queue.TaskInboxPendingReplySend, payload))
}

// pendingComposeOnce is the same for the composed-email handler.
func pendingComposeOnce(t *testing.T, ctx context.Context, core coreapi.Client, s *countingSender,
	pendingID, workspaceID string,
) error {
	t.Helper()
	cc, ok := core.(workerinbox.ComposeCore)
	if !ok {
		t.Fatalf("the remote-sourced client (%T) does not satisfy worker/inbox.ComposeCore", core)
	}
	payload, err := json.Marshal(queue.InboxPendingComposeSendPayload{PendingID: pendingID, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	h := workerinbox.PendingComposeSendHandler(cc, s)
	return h(ctx, asynq.NewTask(queue.TaskInboxPendingComposeSend, payload))
}

// pendingReplyState reads the row's status and the Message-ID it recorded.
func pendingReplyState(t *testing.T, ctx context.Context, f poolFixture, id uuid.UUID) (status, messageID string) {
	t.Helper()
	if err := f.pool.QueryRow(ctx,
		`SELECT status, message_id FROM inbox_pending_replies WHERE id = $1 AND workspace_id = $2`,
		id, f.ws).Scan(&status, &messageID); err != nil {
		t.Fatalf("read pending reply: %v", err)
	}
	return status, messageID
}

func pendingComposeState(t *testing.T, ctx context.Context, f poolFixture, id uuid.UUID) (status, messageID string) {
	t.Helper()
	if err := f.pool.QueryRow(ctx,
		`SELECT status, message_id FROM inbox_pending_composes WHERE id = $1 AND workspace_id = $2`,
		id, f.ws).Scan(&status, &messageID); err != nil {
		t.Fatalf("read pending compose: %v", err)
	}
	return status, messageID
}

// expireLease backdates a claimed row's lease so the reclaim branch fires
// without waiting five real minutes.
func expireLease(t *testing.T, ctx context.Context, f poolFixture, table string, id uuid.UUID) {
	t.Helper()
	// The table name is one of two literals chosen by the caller in this file,
	// never a value, so the interpolation cannot carry input.
	if _, err := f.pool.Exec(ctx,
		`UPDATE `+table+` SET claimed_at = now() - interval '1 hour' WHERE id = $1 AND workspace_id = $2`,
		id, f.ws); err != nil {
		t.Fatalf("expire the lease on %s: %v", table, err)
	}
}

// outboundMessages counts the outbound rows on the thread a pending reply
// belongs to — the record half of the protocol, and the place a duplicated
// RecordInboxReply would show up.
func outboundMessages(t *testing.T, ctx context.Context, f poolFixture, pendingID uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_messages m
		 JOIN inbox_pending_replies p ON p.thread_id = m.thread_id
		 WHERE p.id = $1 AND m.workspace_id = $2 AND m.direction = 'outbound'`,
		pendingID, f.ws).Scan(&n); err != nil {
		t.Fatalf("count outbound messages: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// The happy path, so every scenario below is a deviation from something that
// demonstrably works.
// ---------------------------------------------------------------------------

// A worker with NO DATABASE claims a deferred reply, reads its body off the
// wire, sends it, marks the row sent and records the outbound message — all five
// over HTTP.
func TestARemoteWorkerDeliversAManualReplyWithNoPool(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Happy to help — here are our numbers.")
	worker, _ := faultyFleet(t, f)
	s := &countingSender{}

	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("sent %d times, want 1", s.sends)
	}
	status, messageID := pendingReplyState(t, ctx, f, pendingID)
	if status != inbox.PendingStatusSent || messageID != s.lastMsg {
		t.Errorf("row = %q/%q, want sent/%s", status, messageID, s.lastMsg)
	}
	if got := outboundMessages(t, ctx, f, pendingID); got != 1 {
		t.Errorf("the thread has %d outbound messages, want 1 — the reply was not recorded", got)
	}
}

// The same for a composed email, whose recipients and subject are its own.
func TestARemoteWorkerDeliversAComposedEmailWithNoPool(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingCompose(t, ctx, f, "Hello from Acme")
	worker, _ := faultyFleet(t, f)
	s := &countingSender{}

	if err := pendingComposeOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("sent %d times, want 1", s.sends)
	}
	if status, _ := pendingComposeState(t, ctx, f, pendingID); status != inbox.PendingStatusSent {
		t.Errorf("row status = %q, want sent", status)
	}
}

// ---------------------------------------------------------------------------
// The tests this slice exists for
// ---------------------------------------------------------------------------

// SCENARIO 1 — the CLAIM commits and its response is lost, so the worker loses
// the LEASE AND THE BODY at once.
//
// This is the case with no analogue anywhere else on the seam: every other claim
// answers with an enum, so a lost response costs only the lease. Here the content
// the worker was about to send goes with it.
//
// The retry must NOT send: it re-claims, the row is 'sending' under a live lease,
// the guarded UPDATE matches nothing, and the control plane answers
// ErrInboxPendingNotClaimable — which worker/inbox treats as terminal. The reply
// is therefore DROPPED until the lease expires, and the third act below proves it
// is only deferred rather than destroyed: the body is still on the row, so an
// attempt after the lease delivers it exactly once.
func TestALostPendingReplyClaimLosesTheLeaseAndTheBodyTogether(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Happy to help — here are our numbers.")
	worker, dropper := faultyFleet(t, f)
	s := &countingSender{}

	dropper.setDrop(once(remote.PathInboxPendingReplyClaim))
	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err == nil {
		t.Fatal("the attempt whose claim response was lost reported success")
	}
	if dropped, _ := dropper.counts(); dropped != 1 {
		t.Fatalf("the injector dropped %d responses, want 1 — this test proved nothing", dropped)
	}
	if s.sends != 0 {
		t.Fatalf("sent %d times on an attempt that never received the body, want 0", s.sends)
	}
	// The control plane DID commit the claim. Without this the scenario is just a
	// failed call.
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusSending {
		t.Fatalf("row status = %q, want 'sending' — the control plane committed the claim", status)
	}

	// ACT TWO: the retry, inside the lease. It must stop, not send.
	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the retry inside the lease returned an error (%v); a not-claimable row is terminal, "+
			"and returning an error here retries an undone reply to exhaustion", err)
	}
	if s.sends != 0 {
		t.Fatalf("THE DOUBLE SEND: the retry sent %d times against a claim it never received, want 0", s.sends)
	}
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusSending {
		t.Errorf("row status = %q, want the lease left in place", status)
	}

	// ACT THREE: the lease expires. Nothing was destroyed — the body is on the
	// row, not in the lost response — so the next attempt delivers it ONCE.
	expireLease(t, ctx, f, "inbox_pending_replies", pendingID)
	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the attempt after the lease expired: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("sent %d times in total, want exactly 1", s.sends)
	}
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusSent {
		t.Errorf("row status = %q, want sent", status)
	}
	if got := outboundMessages(t, ctx, f, pendingID); got != 1 {
		t.Errorf("the thread has %d outbound messages, want exactly 1", got)
	}
}

// SCENARIO 2 — THE double send test. The reply went out, the row was marked
// 'sent', and the response was lost.
//
// If this assertion ever reads 2, a customer received the operator's words twice.
//
// The handler is PAST THE DIAL when it calls MarkPendingInboxReplySent, so it
// logs the failure and returns nil rather than retrying — asynq never redelivers
// on its own. What this drives instead is the redelivery that CAN happen: asynq's
// own lease expiring while a slow attempt is still running, which hands the same
// task to a second worker. The row's 'sent' status is what stops it.
func TestALostMarkSentResponseNeverBecomesADoubleSend(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Happy to help — here are our numbers.")
	worker, dropper := faultyFleet(t, f)
	s := &countingSender{}

	dropper.setDrop(once(remote.PathInboxPendingReplySent))
	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the attempt past the dial must NOT return an error (%v): a non-nil return there "+
			"is what makes asynq retry a reply that already left", err)
	}
	if dropped, _ := dropper.counts(); dropped != 1 {
		t.Fatalf("the injector dropped %d responses, want 1", dropped)
	}
	if s.sends != 1 {
		t.Fatalf("sent %d times, want 1 — the reply did go out on this attempt", s.sends)
	}
	// The control plane DID commit the completion. This is what makes a
	// redelivery survivable.
	status, messageID := pendingReplyState(t, ctx, f, pendingID)
	if status != inbox.PendingStatusSent || messageID != s.lastMsg {
		t.Fatalf("row = %q/%q, want sent/%s — the control plane committed before the answer was lost",
			status, messageID, s.lastMsg)
	}

	// The redelivery. A 'sent' row is not claimable by ANY lease, so this is the
	// assertion that holds even after the lease is gone.
	expireLease(t, ctx, f, "inbox_pending_replies", pendingID)
	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the redelivery returned an error: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("THE DOUBLE SEND: sent %d times for one manual reply, want exactly 1", s.sends)
	}
	if got := outboundMessages(t, ctx, f, pendingID); got != 1 {
		t.Errorf("the thread has %d outbound messages, want exactly 1", got)
	}
}

// The same pair for a composed email, which has its own table and its own
// handler but the identical claim discipline.
func TestALostComposeClaimResponseNeverBecomesADoubleSend(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingCompose(t, ctx, f, "Hello from Acme")
	worker, dropper := faultyFleet(t, f)
	s := &countingSender{}

	dropper.setDrop(once(remote.PathInboxPendingComposeClaim))
	if err := pendingComposeOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err == nil {
		t.Fatal("the attempt whose claim response was lost reported success")
	}
	if s.sends != 0 {
		t.Fatalf("sent %d times on an attempt that never received the message, want 0", s.sends)
	}
	if status, _ := pendingComposeState(t, ctx, f, pendingID); status != inbox.PendingStatusSending {
		t.Fatalf("row status = %q, want 'sending'", status)
	}

	if err := pendingComposeOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the retry inside the lease returned an error: %v", err)
	}
	if s.sends != 0 {
		t.Fatalf("THE DOUBLE SEND: the retry sent %d times, want 0", s.sends)
	}

	expireLease(t, ctx, f, "inbox_pending_composes", pendingID)
	if err := pendingComposeOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the attempt after the lease expired: %v", err)
	}
	if s.sends != 1 {
		t.Fatalf("sent %d times in total, want exactly 1", s.sends)
	}
}

// The operator's UNDO, over the wire. This is the ordinary path — the one a
// person takes by clicking Undo — and it must end the task QUIETLY: a returned
// error retries a cancelled reply until asynq gives up and writes the task into
// task_dead_letters, which is served under campaigns:read.
//
// It is also the test that would have caught the sentinel mismatch this branch
// fixes first: before it, worker/inbox's errors.Is check could never match,
// because internal/app/inbox returns its own error value.
func TestACancelledReplyIsNotSentAndEndsTheTaskQuietly(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Actually, never mind.")
	worker, _ := faultyFleet(t, f)
	s := &countingSender{}

	store := inbox.NewPgStore(f.pool)
	if err := store.CancelPendingReply(ctx, f.ws, pendingID); err != nil {
		t.Fatalf("CancelPendingReply: %v", err)
	}

	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("a cancelled reply returned %v; the undo path is not a failure, and returning one "+
			"retries it to exhaustion and captures the task into task_dead_letters", err)
	}
	if s.sends != 0 {
		t.Fatalf("a cancelled reply was sent %d times, want 0", s.sends)
	}
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusCancelled {
		t.Errorf("row status = %q, want it left cancelled", status)
	}
}

// The sentinel itself, over the wire, asserted directly rather than inferred from
// the handler's behaviour — because the handler treats "not claimable" and a
// mapped-away error differently only in whether it RETURNS, and a test that read
// the return alone could pass for the wrong reason.
func TestTheNotClaimableSentinelSurvivesTheWire(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Only once, please.")
	worker, _ := faultyFleet(t, f)
	sends, ok := worker.(InboxSendSource)
	if !ok {
		t.Fatalf("the remote-sourced client (%T) does not satisfy InboxSendSource", worker)
	}

	if _, err := sends.ClaimPendingInboxReply(ctx, f.ws.String(), pendingID.String()); err != nil {
		t.Fatalf("the first claim must win: %v", err)
	}
	_, err := sends.ClaimPendingInboxReply(ctx, f.ws.String(), pendingID.String())
	if !errors.Is(err, coreapi.ErrInboxPendingNotClaimable) {
		t.Fatalf("second claim = %v, want coreapi.ErrInboxPendingNotClaimable across the wire", err)
	}
}

// A thread that lost its inbound message crosses as coreapi.ErrInboxNoInbound,
// which both reply handlers treat as PERMANENT: they fail the row so the outbox
// shows why, rather than retrying a reply that can never be built.
func TestAThreadWithNoInboundMessageFailsTheRowRatherThanRetrying(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Replying to something that is about to vanish.")
	// Delete the inbound message the reply threads on, which is what a deletion
	// between scheduling and sending looks like.
	if _, err := f.pool.Exec(ctx,
		`DELETE FROM inbox_messages m USING inbox_pending_replies p
		 WHERE p.id = $1 AND m.thread_id = p.thread_id AND m.workspace_id = $2 AND m.direction = 'inbound'`,
		pendingID, f.ws); err != nil {
		t.Fatalf("delete the inbound message: %v", err)
	}
	worker, _ := faultyFleet(t, f)
	s := &countingSender{}

	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("a thread with no inbound message returned %v; it is permanent, not retryable", err)
	}
	if s.sends != 0 {
		t.Errorf("sent %d times for a thread with nothing to reply to, want 0", s.sends)
	}
	status, _ := pendingReplyState(t, ctx, f, pendingID)
	if status != inbox.PendingStatusFailed {
		t.Errorf("row status = %q, want failed — the outbox must show why the reply never left", status)
	}
}

// A RELEASE after a transient failure, over the wire: the row goes back to
// 'scheduled' so the retry can claim it at once rather than waiting out the
// lease, and the retry then delivers exactly one message.
func TestATransientSendFailureReleasesTheRowOverTheWire(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Worth retrying.")
	worker, _ := faultyFleet(t, f)
	s := &countingSender{failNext: 1}

	err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String())
	if !errors.Is(err, workerinbox.ErrDeliveryAttemptFailed) {
		t.Fatalf("err = %v, want workerinbox.ErrDeliveryAttemptFailed so asynq retries", err)
	}
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusScheduled {
		t.Fatalf("row status = %q, want it released to 'scheduled' — without the release the retry "+
			"waits out the full lease before it can try again", status)
	}

	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("the retry after a release: %v", err)
	}
	if s.sends != 1 {
		t.Errorf("delivered %d times, want exactly 1 (the failed attempt never reached the provider)", s.sends)
	}
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusSent {
		t.Errorf("row status = %q, want sent", status)
	}
}

// Workspace-pinned across the wire: the control plane applies the same
// workspace_id filter the in-process path applies, so a foreign workspace claims,
// completes and fails ZERO rows (docs/security.md invariant 4).
func TestTheManualSendProtocolIsPinnedToTheRequestedWorkspace(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Belongs to one tenant only.")
	worker, _ := faultyFleet(t, f)
	sends, ok := worker.(InboxSendSource)
	if !ok {
		t.Fatalf("the remote-sourced client (%T) does not satisfy InboxSendSource", worker)
	}
	foreign := f.foreignWS.String()

	// A foreign claim must not move the row, and must not hand over the BODY —
	// which is the part that would be a correspondence disclosure rather than a
	// state bug.
	got, err := sends.ClaimPendingInboxReply(ctx, foreign, pendingID.String())
	if err == nil {
		t.Errorf("a foreign workspace claimed the reply and received %+v", got)
	}
	if got.BodyText != "" {
		t.Errorf("a foreign workspace received the reply body %q", got.BodyText)
	}
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusScheduled {
		t.Errorf("a foreign claim moved the row to %q", status)
	}

	// And the completion half: a foreign workspace cannot mark it sent or fail
	// it. Errors are deliberately not asserted on — a workspace-pinned WHERE
	// matching nothing may be an error or a silent no-op depending on the
	// statement, and which one it is is not the property under test. The ROW is.
	_ = sends.MarkPendingInboxReplySent(ctx, foreign, pendingID.String(), "<x@y>")
	_ = sends.FailPendingInboxReply(ctx, foreign, pendingID.String(), "nope")
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusScheduled {
		t.Errorf("a foreign completion moved the row to %q, want it untouched", status)
	}

	// The reply job read is pinned too: a foreign workspace cannot read a
	// thread's recipient and threading headers.
	if _, err := sends.GetInboxReplyJob(ctx, uuid.NewString(), foreign); err == nil {
		t.Error("GetInboxReplyJob answered for a foreign workspace")
	}
}

// The legacy drain's claim, over the wire, against the REAL idempotency_keys
// table it reuses: a fresh claim wins, a second claim of the same task id loses
// until released, and a release lets the retry's own re-claim win again.
//
// It is drain-only and is deleted with worker/inbox.ReplySendHandler, but while
// it is registered it still sends mail, so it still has to work remotely.
func TestTheLegacyReplyClaimRoundTripsOverTheWire(t *testing.T) {
	ctx, f := setupPool(t)
	worker, _ := faultyFleet(t, f)
	sends, ok := worker.(InboxSendSource)
	if !ok {
		t.Fatalf("the remote-sourced client (%T) does not satisfy InboxSendSource", worker)
	}
	taskID := "inboxreply:" + uuid.NewString() + ":1700000000"

	claimed, err := sends.ClaimInboxReply(ctx, f.ws.String(), taskID)
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v, want true/nil", claimed, err)
	}
	claimed, err = sends.ClaimInboxReply(ctx, f.ws.String(), taskID)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if claimed {
		t.Fatal("a second claim of the SAME task id won; a lost response would then double-send")
	}
	if err := sends.ReleaseInboxReply(ctx, f.ws.String(), taskID); err != nil {
		t.Fatalf("release: %v", err)
	}
	claimed, err = sends.ClaimInboxReply(ctx, f.ws.String(), taskID)
	if err != nil || !claimed {
		t.Fatalf("re-claim after release: claimed=%v err=%v, want true/nil", claimed, err)
	}
}

// FAIL CLOSED where it matters: the rows ARE in Postgres and the control plane is
// gone. Every call must return an error — never a claim it did not take, and
// never an empty reply job a worker would dial with.
func TestEveryManualSendFailsClosedWhenTheControlPlaneDies(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Never leaves.")
	upstream := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteOutcomeCore(t, upstream.URL)
	sends, ok := worker.(InboxSendSource)
	if !ok {
		t.Fatalf("the remote-sourced client (%T) does not satisfy InboxSendSource", worker)
	}
	upstream.Close()

	if got, err := sends.ClaimPendingInboxReply(ctx, f.ws.String(), pendingID.String()); err == nil {
		t.Error("ClaimPendingInboxReply answered with the control plane down")
	} else if got.BodyText != "" || got.ThreadID != "" {
		t.Errorf("ClaimPendingInboxReply returned %+v alongside its error, want the zero value", got)
	}
	if err := sends.MarkPendingInboxReplySent(ctx, f.ws.String(), pendingID.String(), "<m@x>"); err == nil {
		t.Error("MarkPendingInboxReplySent answered with the control plane down")
	}
	if claimed, err := sends.ClaimInboxReply(ctx, f.ws.String(), "inboxreply:x:1"); err == nil {
		t.Error("ClaimInboxReply answered with the control plane down")
	} else if claimed {
		t.Error("ClaimInboxReply returned claimed=true alongside its error — a send on a claim nobody took")
	}
	// And nothing moved.
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusScheduled {
		t.Errorf("row status = %q after a run against a dead control plane, want scheduled", status)
	}
}

// The suppression re-check still runs on the remote path, and it runs AFTER the
// claim — which is where it earns its keep, because the deferral window makes
// "the contact unsubscribed since this was scheduled" a real case rather than a
// theory. A suppressed recipient FAILS the row (permanent), never releases it.
func TestASuppressedRecipientFailsTheRowOverTheWire(t *testing.T) {
	ctx, f := setupPool(t)
	pendingID := seedPendingReply(t, ctx, f, "Too late — they unsubscribed.")
	worker, _ := faultyFleet(t, f)
	s := &countingSender{}

	// lead@x.test is the From: address of the inbound message seedThreadForReply
	// writes, and therefore this reply's recipient.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO suppression (workspace_id, email, reason) VALUES ($1, $2, 'unsubscribe')
		 ON CONFLICT DO NOTHING`, f.ws, "lead@x.test"); err != nil {
		t.Fatalf("suppress the recipient: %v", err)
	}

	if err := pendingReplyOnce(t, ctx, worker, s, pendingID.String(), f.ws.String()); err != nil {
		t.Fatalf("a suppressed recipient returned %v; it is permanent, not retryable", err)
	}
	if s.sends != 0 {
		t.Fatalf("sent %d times to a suppressed recipient, want 0", s.sends)
	}
	if status, _ := pendingReplyState(t, ctx, f, pendingID); status != inbox.PendingStatusFailed {
		t.Errorf("row status = %q, want failed", status)
	}
}

// The elapsed-time assertion the lease scenarios rest on: PendingReplyLeaseSeconds
// is long enough that a retry inside it is the ORDINARY case, not a race. Stated
// here rather than assumed, because the "drop until the lease expires" trade is
// only acceptable while the lease is minutes rather than hours.
func TestThePendingReplyLeaseIsMinutesNotHours(t *testing.T) {
	lease := time.Duration(inbox.PendingReplyLeaseSeconds) * time.Second
	if lease < time.Minute || lease > 15*time.Minute {
		t.Errorf("PendingReplyLeaseSeconds = %v; a lost claim response strands a human's reply for "+
			"this long, so the trade in internal/coreapi/remote/inboxsends.go assumes minutes", lease)
	}
}
