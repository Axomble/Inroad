//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// The deferred-send state machine against real Postgres.
//
// These are the highest-stakes queries in the domain: every guard here is what
// stands between "the operator's undo worked" and "the reply went out anyway",
// or between one delivery and two. The Go fake mirrors each guard, but only the
// database can prove the WHERE clauses actually say what the fake assumes.

// createDuePending inserts a row already past its send_after, so a claim can be
// attempted without waiting. Written through the store (not raw SQL) so the
// self-enforcing INSERT … SELECT is exercised too.
func createDuePending(t *testing.T, ctx context.Context, f *fixture, threadID uuid.UUID) inbox.PendingReply {
	t.Helper()
	row, err := f.store.CreatePendingReply(ctx, inbox.CreatePendingReplyInput{
		WorkspaceID: f.ws, ThreadID: threadID, BodyText: "on it",
		SendAfter: time.Now().UTC().Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("CreatePendingReply: %v", err)
	}
	return row
}

func TestPendingReplyLifecycleAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-life@s.test>", time.Now().UTC())

	row := createDuePending(t, ctx, f, th.ID)
	if row.Status != inbox.PendingStatusScheduled {
		t.Fatalf("Status = %q, want scheduled", row.Status)
	}
	if !row.Cancellable() {
		t.Error("a scheduled reply is not cancellable")
	}

	if err := f.store.ClaimPendingReply(ctx, f.ws, row.ID); err != nil {
		t.Fatalf("ClaimPendingReply: %v", err)
	}
	claimed, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if claimed.Status != inbox.PendingStatusSending {
		t.Errorf("Status after claim = %q, want sending", claimed.Status)
	}
	if claimed.Cancellable() {
		t.Error("a claimed reply still reports itself cancellable")
	}

	if err := f.store.MarkPendingReplySent(ctx, f.ws, row.ID, "<mid@sender.test>"); err != nil {
		t.Fatalf("MarkPendingReplySent: %v", err)
	}
	sent, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if sent.Status != inbox.PendingStatusSent || sent.MessageID != "<mid@sender.test>" || sent.SentAt == nil {
		t.Errorf("sent row = %+v, want status sent with a message id and sent_at", sent)
	}
}

// The guard that makes undo real: a cancelled row must never become claimable,
// however many times the delayed task fires.
func TestCancelledPendingReplyIsNeverClaimableAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-cancel@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)

	if err := f.store.CancelPendingReply(ctx, f.ws, row.ID); err != nil {
		t.Fatalf("CancelPendingReply: %v", err)
	}
	for range 3 {
		if err := f.store.ClaimPendingReply(ctx, f.ws, row.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
			t.Fatalf("a cancelled reply was claimable: %v", err)
		}
	}
	after, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if after.Status != inbox.PendingStatusCancelled {
		t.Errorf("Status = %q, want it to stay cancelled", after.Status)
	}
}

// Cancel is guarded on 'scheduled': once a worker holds the row the SMTP
// conversation may be open, so reporting a successful cancel would be a lie.
func TestCancelIsRefusedOnceClaimedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-late@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)

	if err := f.store.ClaimPendingReply(ctx, f.ws, row.ID); err != nil {
		t.Fatalf("ClaimPendingReply: %v", err)
	}
	if err := f.store.CancelPendingReply(ctx, f.ws, row.ID); !errors.Is(err, inbox.ErrNotFound) {
		// The store reports "no rows matched"; the Service turns that into
		// ErrPendingNotCancellable after a second read.
		t.Errorf("CancelPendingReply after claim = %v, want ErrNotFound from the store", err)
	}
	still, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if still.Status != inbox.PendingStatusSending {
		t.Errorf("Status = %q, want it to stay sending", still.Status)
	}
}

// Exactly-once delivery: the second claim must be refused, or the reply is sent
// twice.
func TestOnlyOneWorkerCanClaimAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-once@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)

	if err := f.store.ClaimPendingReply(ctx, f.ws, row.ID); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := f.store.ClaimPendingReply(ctx, f.ws, row.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Errorf("second claim = %v, want ErrPendingNotClaimable", err)
	}
}

// The send_after guard: a task delivered early (asynq firing ahead, or a retry
// of an earlier attempt) must wait rather than send.
func TestClaimRefusedBeforeSendAfterAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-early@s.test>", time.Now().UTC())

	row, err := f.store.CreatePendingReply(ctx, inbox.CreatePendingReplyInput{
		WorkspaceID: f.ws, ThreadID: th.ID, BodyText: "later",
		SendAfter: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreatePendingReply: %v", err)
	}
	if err := f.store.ClaimPendingReply(ctx, f.ws, row.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Errorf("claiming an undue reply = %v, want ErrPendingNotClaimable", err)
	}
}

// A released row is immediately re-claimable, so a momentary SMTP blip costs one
// retry rather than the whole lease.
func TestReleaseMakesAReplyClaimableAgainAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-release@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)

	if err := f.store.ClaimPendingReply(ctx, f.ws, row.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := f.store.ReleasePendingReply(ctx, f.ws, row.ID, "smtp timeout"); err != nil {
		t.Fatalf("ReleasePendingReply: %v", err)
	}

	released, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if released.Status != inbox.PendingStatusScheduled {
		t.Errorf("Status = %q, want scheduled", released.Status)
	}
	if released.LastError != "smtp timeout" {
		t.Errorf("LastError = %q, want the release reason", released.LastError)
	}
	// send_after must NOT have moved: the reply is already late, and pushing it
	// out would compound the delay the failure caused.
	if !released.SendAfter.Equal(row.SendAfter) {
		t.Errorf("SendAfter moved from %v to %v on release", row.SendAfter, released.SendAfter)
	}
	if err := f.store.ClaimPendingReply(ctx, f.ws, row.ID); err != nil {
		t.Errorf("a released reply was not immediately re-claimable: %v", err)
	}
}

// MarkSent is guarded on 'sending', so only the claimer can complete it.
func TestMarkSentRequiresTheClaimAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-mark@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)

	if err := f.store.MarkPendingReplySent(ctx, f.ws, row.ID, "<mid@x>"); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("MarkPendingReplySent without a claim = %v, want ErrNotFound", err)
	}
	unchanged, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if unchanged.Status != inbox.PendingStatusScheduled {
		t.Errorf("Status = %q, want it untouched", unchanged.Status)
	}
}

// A failed row survives so the outbox can show what happened.
func TestFailedPendingReplyKeepsItsReasonAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-fail@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)

	if err := f.store.FailPendingReply(ctx, f.ws, row.ID, "recipient unsubscribed"); err != nil {
		t.Fatalf("FailPendingReply: %v", err)
	}
	failed, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("the failed row did not survive: %v", err)
	}
	if failed.Status != inbox.PendingStatusFailed || failed.LastError != "recipient unsubscribed" {
		t.Errorf("failed row = %+v, want status failed with the reason", failed)
	}
	// ...and it leaves the outbox, which lists only what is still waiting.
	waiting, err := f.store.ListPendingReplies(ctx, f.ws, inbox.MaxPendingReplyPageLimit)
	if err != nil {
		t.Fatalf("ListPendingReplies: %v", err)
	}
	for _, item := range waiting {
		if item.ID == row.ID {
			t.Error("a failed reply is still listed as waiting")
		}
	}
}

// A provider's error message can be very long; the column must not be a dumping
// ground.
func TestLongFailureReasonIsTruncatedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-long@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)

	huge := make([]byte, 5000)
	for i := range huge {
		huge[i] = 'x'
	}
	if err := f.store.FailPendingReply(ctx, f.ws, row.ID, string(huge)); err != nil {
		t.Fatalf("FailPendingReply: %v", err)
	}
	failed, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if len(failed.LastError) > 500 {
		t.Errorf("LastError is %d bytes, want it truncated to 500", len(failed.LastError))
	}
}

// Invariant: never touch another workspace's send (docs/security.md). This one
// matters more than most — a cross-tenant claim would deliver one workspace's
// mail from another's mailbox.
func TestPendingReplyIsWorkspaceScopedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-iso@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)
	foreignWS, _ := seedTenant(t, ctx, f.q)

	if _, err := f.store.GetPendingReply(ctx, foreignWS, row.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("GetPendingReply(foreign) = %v, want ErrNotFound", err)
	}
	if err := f.store.ClaimPendingReply(ctx, foreignWS, row.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Errorf("ClaimPendingReply(foreign) = %v, want ErrPendingNotClaimable", err)
	}
	if err := f.store.CancelPendingReply(ctx, foreignWS, row.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("CancelPendingReply(foreign) = %v, want ErrNotFound", err)
	}
	// ...and the owner's row is untouched by all three.
	still, err := f.store.GetPendingReply(ctx, f.ws, row.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if still.Status != inbox.PendingStatusScheduled {
		t.Errorf("Status = %q, want scheduled — a foreign caller changed it", still.Status)
	}

	foreignList, err := f.store.ListPendingReplies(ctx, foreignWS, inbox.MaxPendingReplyPageLimit)
	if err != nil {
		t.Fatalf("ListPendingReplies(foreign): %v", err)
	}
	if len(foreignList) != 0 {
		t.Errorf("a foreign workspace sees %d queued replies, want 0", len(foreignList))
	}
}

// The INSERT … SELECT's self-enforcing tenancy: a foreign thread id must write
// ZERO rows, not a row that fails a later check.
func TestCreatePendingReplyRefusesAForeignThreadAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	foreignWS, foreignMailbox := seedTenant(t, ctx, f.q)

	foreign, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: foreignWS, MailboxID: foreignMailbox, RootMessageID: "<pending-foreign@s.test>",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed foreign thread: %v", err)
	}

	_, err = f.store.CreatePendingReply(ctx, inbox.CreatePendingReplyInput{
		WorkspaceID: f.ws, ThreadID: foreign.ID, BodyText: "not yours",
		SendAfter: time.Now().UTC().Add(time.Minute),
	})
	if !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_pending_replies WHERE thread_id = $1`, foreign.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d rows written for a foreign thread, want 0", rows)
	}
}

// The outbox lists only what is still waiting, soonest first.
func TestListPendingRepliesOrdersBySendAfterAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-order@s.test>", time.Now().UTC())
	now := time.Now().UTC()

	later, err := f.store.CreatePendingReply(ctx, inbox.CreatePendingReplyInput{
		WorkspaceID: f.ws, ThreadID: th.ID, BodyText: "b", SendAfter: now.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("create later: %v", err)
	}
	sooner, err := f.store.CreatePendingReply(ctx, inbox.CreatePendingReplyInput{
		WorkspaceID: f.ws, ThreadID: th.ID, BodyText: "a", SendAfter: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create sooner: %v", err)
	}

	page, err := f.store.ListPendingReplies(ctx, f.ws, inbox.MaxPendingReplyPageLimit)
	if err != nil {
		t.Fatalf("ListPendingReplies: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("%d queued replies, want 2", len(page))
	}
	if page[0].ID != sooner.ID || page[1].ID != later.ID {
		t.Errorf("order = [%s %s], want [%s %s]", page[0].ID, page[1].ID, sooner.ID, later.ID)
	}
	// The join must populate the display fields the outbox renders.
	if page[0].ThreadSubject == "" && th.Subject != "" {
		t.Error("thread_subject was not joined")
	}
}

// Deleting a thread takes its queued replies with it, rather than leaving rows
// that would try to send into nothing.
func TestDeletingAThreadCascadesToItsPendingRepliesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-cascade@s.test>", time.Now().UTC())
	row := createDuePending(t, ctx, f, th.ID)

	if _, err := f.pool.Exec(ctx, `DELETE FROM inbox_threads WHERE id = $1`, th.ID); err != nil {
		t.Fatalf("delete thread: %v", err)
	}
	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_pending_replies WHERE id = $1`, row.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d orphaned queued replies after deleting the thread, want 0", rows)
	}
}

func TestUndoWindowSettingsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	// A workspace that has never configured one gets the default, not an error.
	window, err := f.store.UndoWindow(ctx, f.ws)
	if err != nil {
		t.Fatalf("UndoWindow: %v", err)
	}
	if window != inbox.DefaultUndoSendSeconds*time.Second {
		t.Errorf("default window = %v, want %ds", window, inbox.DefaultUndoSendSeconds)
	}

	if err := f.store.SetUndoWindow(ctx, f.ws, 45); err != nil {
		t.Fatalf("SetUndoWindow: %v", err)
	}
	window, err = f.store.UndoWindow(ctx, f.ws)
	if err != nil {
		t.Fatalf("UndoWindow: %v", err)
	}
	if window != 45*time.Second {
		t.Errorf("window = %v, want 45s", window)
	}

	// Upsert, not insert: setting it twice must not conflict.
	if err := f.store.SetUndoWindow(ctx, f.ws, 0); err != nil {
		t.Fatalf("second SetUndoWindow: %v", err)
	}
	window, err = f.store.UndoWindow(ctx, f.ws)
	if err != nil {
		t.Fatalf("UndoWindow: %v", err)
	}
	if window != 0 {
		t.Errorf("window = %v, want 0 (undo disabled)", window)
	}

	// Another workspace is unaffected.
	foreignWS, _ := seedTenant(t, ctx, f.q)
	foreignWindow, err := f.store.UndoWindow(ctx, foreignWS)
	if err != nil {
		t.Fatalf("UndoWindow(foreign): %v", err)
	}
	if foreignWindow != inbox.DefaultUndoSendSeconds*time.Second {
		t.Errorf("foreign window = %v, want the default", foreignWindow)
	}
}

// A thread with a reply in flight reports it; once terminal, it does not.
func TestPendingReplyForThreadAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<pending-thread@s.test>", time.Now().UTC())

	if _, err := f.store.PendingReplyForThread(ctx, f.ws, th.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("a thread with nothing queued = %v, want ErrNotFound", err)
	}

	row := createDuePending(t, ctx, f, th.ID)
	found, err := f.store.PendingReplyForThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("PendingReplyForThread: %v", err)
	}
	if found.ID != row.ID {
		t.Errorf("found %s, want %s", found.ID, row.ID)
	}

	if err := f.store.CancelPendingReply(ctx, f.ws, row.ID); err != nil {
		t.Fatalf("CancelPendingReply: %v", err)
	}
	if _, err := f.store.PendingReplyForThread(ctx, f.ws, th.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("a cancelled reply is still reported in flight: %v", err)
	}
}
