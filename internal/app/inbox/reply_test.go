package inbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// fakeReplyEnqueuer is Reply's mocked enqueue seam: it never touches Redis,
// it just records the payload it was asked to enqueue.
type fakeReplyEnqueuer struct {
	threadID, bodyText, workspaceID string
	calls                           int
	err                             error
}

func (f *fakeReplyEnqueuer) EnqueueInboxReplySend(threadID, bodyText, workspaceID string) error {
	f.threadID, f.bodyText, f.workspaceID = threadID, bodyText, workspaceID
	f.calls++
	return f.err
}

// fakeSuppressionChecker is Reply's mocked SuppressionChecker: suppressed/err
// are set per test, lastEmail records the address it was last asked about.
type fakeSuppressionChecker struct {
	suppressed bool
	err        error
	lastEmail  string
}

func (f *fakeSuppressionChecker) IsSuppressed(_ context.Context, _ uuid.UUID, email string) (bool, error) {
	f.lastEmail = email
	return f.suppressed, f.err
}

// seedThreadWithInbound seeds a thread with one inbound message from
// fromEmail and returns its id.
func seedThreadWithInbound(store *fakeStore, ws uuid.UUID, fromEmail string) uuid.UUID {
	th := inbox.Thread{ID: uuid.New(), WorkspaceID: ws, MailboxID: uuid.New(), Subject: "Hi", Unread: true}
	store.threads[th.ID] = th
	store.messages[th.ID] = []inbox.Message{
		{ThreadID: th.ID, Direction: "inbound", FromEmail: fromEmail, MessageID: "msg-1", OccurredAt: time.Now()},
	}
	return th.ID
}

func TestReplyUnknownThreadIsNotFound(t *testing.T) {
	svc := inbox.NewService(newFakeStore())
	err := svc.Reply(context.Background(), uuid.New(), uuid.New(), "hello")
	if !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("Reply on unknown thread = %v, want ErrNotFound", err)
	}
}

func TestReplyCrossWorkspaceThreadIsNotFound(t *testing.T) {
	store := newFakeStore()
	wsA, wsB := uuid.New(), uuid.New()
	threadID := seedThreadWithInbound(store, wsA, "lead@x.test")
	svc := inbox.NewService(store)
	if err := svc.Reply(context.Background(), wsB, threadID, "hello"); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("Reply across workspaces = %v, want ErrNotFound", err)
	}
}

func TestReplyWithNoInboundMessageIsConflict(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	th := inbox.Thread{ID: uuid.New(), WorkspaceID: ws, MailboxID: uuid.New(), Subject: "Hi"}
	store.threads[th.ID] = th // no messages seeded at all
	svc := inbox.NewService(store)
	if err := svc.Reply(context.Background(), ws, th.ID, "hello"); !errors.Is(err, inbox.ErrNoInboundMessage) {
		t.Fatalf("Reply with no inbound message = %v, want ErrNoInboundMessage", err)
	}
}

func TestReplyToSuppressedRecipientIsConflict(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithInbound(store, ws, "lead@x.test")
	checker := &fakeSuppressionChecker{suppressed: true}
	svc := inbox.NewService(store, inbox.WithSuppressionChecker(checker), inbox.WithReplyEnqueuer(&fakeReplyEnqueuer{}))

	if err := svc.Reply(context.Background(), ws, threadID, "hello"); !errors.Is(err, inbox.ErrRecipientSuppressed) {
		t.Fatalf("Reply to suppressed recipient = %v, want ErrRecipientSuppressed", err)
	}
	if checker.lastEmail != "lead@x.test" {
		t.Fatalf("suppression checked %q, want the latest inbound message's From: address", checker.lastEmail)
	}
}

// A suppression-checker error fails closed, like campaign.Service's identical
// rule: never treated as "not suppressed".
func TestReplySuppressionCheckerErrorPropagates(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithInbound(store, ws, "lead@x.test")
	boom := errors.New("redis down")
	svc := inbox.NewService(store,
		inbox.WithSuppressionChecker(&fakeSuppressionChecker{err: boom}),
		inbox.WithReplyEnqueuer(&fakeReplyEnqueuer{}))

	if err := svc.Reply(context.Background(), ws, threadID, "hello"); !errors.Is(err, boom) {
		t.Fatalf("Reply with a suppression-checker error = %v, want %v", err, boom)
	}
}

func TestReplyRejectsEmptyOrOversizedBody(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithInbound(store, ws, "lead@x.test")
	svc := inbox.NewService(store, inbox.WithReplyEnqueuer(&fakeReplyEnqueuer{}))

	if err := svc.Reply(context.Background(), ws, threadID, ""); !errors.Is(err, inbox.ErrReplyBodyInvalid) {
		t.Fatalf("Reply with empty body = %v, want ErrReplyBodyInvalid", err)
	}
	oversized := strings.Repeat("a", 100001)
	if err := svc.Reply(context.Background(), ws, threadID, oversized); !errors.Is(err, inbox.ErrReplyBodyInvalid) {
		t.Fatalf("Reply with an oversized body = %v, want ErrReplyBodyInvalid", err)
	}
}

// The happy path: enqueued with the right args, and the thread is marked
// read as a side effect (the caller who just replied has, by construction,
// seen it).
func TestReplyEnqueuesAndMarksThreadRead(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithInbound(store, ws, "lead@x.test")
	enq := &fakeReplyEnqueuer{}
	checker := &fakeSuppressionChecker{suppressed: false}
	svc := inbox.NewService(store, inbox.WithSuppressionChecker(checker), inbox.WithReplyEnqueuer(enq))

	if err := svc.Reply(context.Background(), ws, threadID, "thanks for reaching out"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if enq.calls != 1 {
		t.Fatalf("enqueue called %d times, want 1", enq.calls)
	}
	if enq.threadID != threadID.String() || enq.bodyText != "thanks for reaching out" || enq.workspaceID != ws.String() {
		t.Fatalf("enqueue args = (%q,%q,%q), want (%q,%q,%q)",
			enq.threadID, enq.bodyText, enq.workspaceID, threadID.String(), "thanks for reaching out", ws.String())
	}
	got, err := svc.GetThread(context.Background(), ws, threadID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.Thread.Unread {
		t.Fatal("thread still unread after a successful Reply")
	}
}

// A SetUnread failure AFTER a successful enqueue must be swallowed (logged,
// not returned): the send is already queued, so a 500 here would make the
// caller retry and risk a second reply actually going out — the enqueue's
// unix-second dedup key does not catch a retry seconds later. This mirrors
// how the worker treats a post-send RecordInboxReply failure.
func TestReplySwallowsAMarkReadFailureAfterASuccessfulEnqueue(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithInbound(store, ws, "lead@x.test")
	store.setUnreadErr = errors.New("db down")
	enq := &fakeReplyEnqueuer{}
	svc := inbox.NewService(store, inbox.WithReplyEnqueuer(enq))

	if err := svc.Reply(context.Background(), ws, threadID, "hello"); err != nil {
		t.Fatalf("Reply: %v, want nil (the send is queued; a mark-read failure must not surface as an error)", err)
	}
	if enq.calls != 1 {
		t.Fatalf("enqueue called %d times, want 1 (the enqueue itself must still have happened)", enq.calls)
	}
}

// Without a ReplyEnqueuer wired, Reply must fail rather than silently drop
// the reply -- unlike the optional SuppressionChecker, there is no safe
// "unwired" default for actually sending mail.
func TestReplyWithoutAnEnqueuerFails(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithInbound(store, ws, "lead@x.test")
	svc := inbox.NewService(store)

	if err := svc.Reply(context.Background(), ws, threadID, "hello"); err == nil {
		t.Fatal("Reply with no ReplyEnqueuer wired = nil error, want an error")
	}
}
