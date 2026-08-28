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

// fakeSuppressionChecker is the mocked SuppressionChecker: suppressed/err are
// set per test, lastEmail records the address it was last asked about.
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

// replyDeps wires the immediate-reply path the way cmd/inroad does: a
// pending-reply ROW store plus the pointer-carrying enqueuer. Reply no longer
// has an enqueue seam of its own — an immediate reply is a queued row whose
// send_after has already passed, which is the entire point of this change: the
// body lives in the row, and the task carries only its id.
type replyDeps struct {
	pending *fakePendingStore
	enq     *recordingPendingEnqueuer
	opts    []inbox.ServiceOption
}

func newReplyDeps(store *fakeStore, clock func() time.Time, extra ...inbox.ServiceOption) replyDeps {
	if clock == nil {
		clock = time.Now
	}
	pending := newFakePendingStore(clock, store)
	enq := &recordingPendingEnqueuer{}
	opts := append([]inbox.ServiceOption{
		inbox.WithPendingReplyStore(pending),
		inbox.WithPendingReplyEnqueuer(enq),
		inbox.WithClock(clock),
	}, extra...)
	return replyDeps{pending: pending, enq: enq, opts: opts}
}

type replyFixture struct {
	svc      *inbox.Service
	store    *fakeStore
	deps     replyDeps
	now      time.Time
	threadID uuid.UUID
}

func newReplyFixture(t *testing.T, extra ...inbox.ServiceOption) *replyFixture {
	t.Helper()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	threadID := seedThreadWithInbound(store, testWS, "lead@x.test")
	deps := newReplyDeps(store, func() time.Time { return now }, extra...)
	return &replyFixture{
		svc: inbox.NewService(store, deps.opts...), store: store,
		deps: deps, now: now, threadID: threadID,
	}
}

// scheduled returns the one row Reply created, failing if there is not exactly
// one — "did it write a row at all" is half of what these tests assert.
func (f *replyFixture) scheduled(t *testing.T) inbox.PendingReply {
	t.Helper()
	rows, err := f.svc.ListPendingReplies(context.Background(), testWS, 100)
	if err != nil {
		t.Fatalf("ListPendingReplies: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d queued replies, want exactly 1", len(rows))
	}
	return rows[0]
}

// THE SHAPE CHANGE. Reply writes the body to a row and hands the queue that
// row's id. Nothing about the reply's text reaches the task — which is what
// keeps it out of task_dead_letters, and therefore out of a GET /dead-letters
// response served under campaigns:read.
func TestReplyQueuesARowAndEnqueuesOnlyItsID(t *testing.T) {
	f := newReplyFixture(t)

	if err := f.svc.Reply(context.Background(), testWS, f.threadID, "thanks for reaching out", nil); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	row := f.scheduled(t)
	if row.BodyText != "thanks for reaching out" {
		t.Errorf("row body = %q, want the operator's text stored on the row", row.BodyText)
	}
	if row.ThreadID != f.threadID || row.WorkspaceID != testWS {
		t.Errorf("row = %+v, want it pinned to the thread and workspace", row)
	}
	if len(f.deps.enq.calls) != 1 {
		t.Fatalf("%d enqueue calls, want 1", len(f.deps.enq.calls))
	}
	call := f.deps.enq.calls[0]
	if call.pendingID != row.ID.String() {
		t.Errorf("enqueued %q, want the row id %q", call.pendingID, row.ID)
	}
	if call.workspaceID != testWS.String() {
		t.Errorf("enqueued workspace %q, want %q", call.workspaceID, testWS)
	}
	if !call.sendAfter.Equal(row.SendAfter) {
		t.Errorf("enqueued for %v but the row says %v", call.sendAfter, row.SendAfter)
	}
}

// An immediate reply's send_after must be STRICTLY in the past, not "now".
//
// ClaimInboxPendingReply guards on `send_after <= now()` evaluated on the
// DATABASE clock, while this instant is stamped from the APP clock. Equal-to-now
// therefore loses to any forward skew between the two: the task fires, the claim
// matches no row, and — because there is no sweeper over stranded 'scheduled'
// rows (see ScheduleReply's own note) — the reply never leaves and the operator
// is told it was sent.
func TestReplySendAfterIsStrictlyInThePastToSurviveClockSkew(t *testing.T) {
	f := newReplyFixture(t)

	if err := f.svc.Reply(context.Background(), testWS, f.threadID, "on it", nil); err != nil {
		t.Fatalf("Reply: %v", err)
	}

	row := f.scheduled(t)
	if !row.SendAfter.Before(f.now) {
		t.Fatalf("send_after = %v, want strictly before now (%v): an equal instant is unclaimable "+
			"under any forward skew between the app and database clocks", row.SendAfter, f.now)
	}
	// And the consequence, asserted rather than described: the row a worker
	// picks up the instant its task fires is claimable.
	if err := f.svc.ClaimPendingReply(context.Background(), testWS, row.ID); err != nil {
		t.Fatalf("ClaimPendingReply on a just-created immediate reply: %v, want it claimable at once", err)
	}
}

// The thread is marked read: whoever just replied has, by construction, seen it.
func TestReplyMarksTheThreadRead(t *testing.T) {
	f := newReplyFixture(t)

	if err := f.svc.Reply(context.Background(), testWS, f.threadID, "hello", nil); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	detail, err := f.svc.GetThread(context.Background(), testWS, f.threadID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if detail.Thread.Unread {
		t.Fatal("thread still unread after a successful Reply")
	}
}

// Every rejection must land BEFORE a row exists. A row written and then
// abandoned would sit in the operator's outbox offering an Undo for mail that
// was never going anywhere.
func TestReplyRejectionsHappenBeforeAnyRowIsCreated(t *testing.T) {
	suppressed := &fakeSuppressionChecker{suppressed: true}
	cases := []struct {
		name  string
		build func(t *testing.T) (*replyFixture, uuid.UUID, string)
		want  error
	}{
		{
			name: "empty body",
			build: func(t *testing.T) (*replyFixture, uuid.UUID, string) {
				f := newReplyFixture(t)
				return f, f.threadID, ""
			},
			want: inbox.ErrReplyBodyInvalid,
		},
		{
			name: "oversized body",
			build: func(t *testing.T) (*replyFixture, uuid.UUID, string) {
				f := newReplyFixture(t)
				return f, f.threadID, strings.Repeat("a", 100001)
			},
			want: inbox.ErrReplyBodyInvalid,
		},
		{
			name: "unknown thread",
			build: func(t *testing.T) (*replyFixture, uuid.UUID, string) {
				return newReplyFixture(t), uuid.New(), "hi"
			},
			want: inbox.ErrNotFound,
		},
		{
			name: "thread with no inbound message",
			build: func(t *testing.T) (*replyFixture, uuid.UUID, string) {
				f := newReplyFixture(t)
				bare := inbox.Thread{ID: uuid.New(), WorkspaceID: testWS, MailboxID: uuid.New(), Subject: "Hi"}
				f.store.threads[bare.ID] = bare
				return f, bare.ID, "hi"
			},
			want: inbox.ErrNoInboundMessage,
		},
		{
			name: "suppressed recipient",
			build: func(t *testing.T) (*replyFixture, uuid.UUID, string) {
				f := newReplyFixture(t, inbox.WithSuppressionChecker(suppressed))
				return f, f.threadID, "hi"
			},
			want: inbox.ErrRecipientSuppressed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, threadID, body := tc.build(t)
			if err := f.svc.Reply(context.Background(), testWS, threadID, body, nil); !errors.Is(err, tc.want) {
				t.Fatalf("Reply = %v, want %v", err, tc.want)
			}
			rows, err := f.svc.ListPendingReplies(context.Background(), testWS, 100)
			if err != nil {
				t.Fatalf("ListPendingReplies: %v", err)
			}
			if len(rows) != 0 {
				t.Errorf("a rejected reply left %d rows queued", len(rows))
			}
			if len(f.deps.enq.calls) != 0 {
				t.Errorf("a rejected reply enqueued %d tasks", len(f.deps.enq.calls))
			}
		})
	}
	if suppressed.lastEmail != "lead@x.test" {
		t.Errorf("suppression checked %q, want the latest inbound message's From: address", suppressed.lastEmail)
	}
}

// A cross-workspace thread is indistinguishable from one that does not exist.
func TestReplyCrossWorkspaceThreadIsNotFound(t *testing.T) {
	f := newReplyFixture(t)
	if err := f.svc.Reply(context.Background(), uuid.New(), f.threadID, "hello", nil); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("Reply across workspaces = %v, want ErrNotFound", err)
	}
}

// A suppression-checker error fails closed, like campaign.Service's identical
// rule: never treated as "not suppressed".
func TestReplySuppressionCheckerErrorPropagates(t *testing.T) {
	boom := errors.New("redis down")
	f := newReplyFixture(t, inbox.WithSuppressionChecker(&fakeSuppressionChecker{err: boom}))

	if err := f.svc.Reply(context.Background(), testWS, f.threadID, "hello", nil); !errors.Is(err, boom) {
		t.Fatalf("Reply with a suppression-checker error = %v, want %v", err, boom)
	}
}

// The orphan compensation: the row exists but nothing will ever claim it, so it
// is marked failed rather than left 'scheduled' — which would show the operator
// a reply in flight, counting against their outbox cap, for mail that is never
// going to leave.
func TestReplyEnqueueFailureMarksTheRowFailed(t *testing.T) {
	f := newReplyFixture(t)
	f.deps.enq.err = errors.New("redis down")

	err := f.svc.Reply(context.Background(), testWS, f.threadID, "on it", nil)
	if err == nil {
		t.Fatal("Reply reported success despite an enqueue failure")
	}
	if !errors.Is(err, f.deps.enq.err) {
		t.Errorf("error = %v, want it to wrap the enqueue failure", err)
	}
	// Not in the outbox any more, because the outbox is what is still waiting.
	if rows, listErr := f.svc.ListPendingReplies(context.Background(), testWS, 100); listErr != nil || len(rows) != 0 {
		t.Errorf("outbox = %d rows (err %v), want the orphan marked failed and gone", len(rows), listErr)
	}
	var failed inbox.PendingReply
	for _, row := range f.deps.pending.rows {
		failed = row
	}
	if failed.Status != inbox.PendingStatusFailed {
		t.Errorf("row status = %q, want %q", failed.Status, inbox.PendingStatusFailed)
	}
	if failed.LastError == "" {
		t.Error("the failed row carries no reason, so the outbox cannot say what happened")
	}
}

// A SetUnread failure AFTER the row is queued must be swallowed (logged, not
// returned): the send is committed, so a 500 here would make the caller retry
// and risk a second reply actually going out.
func TestReplySwallowsAMarkReadFailureAfterTheRowIsQueued(t *testing.T) {
	f := newReplyFixture(t)
	f.store.setUnreadErr = errors.New("db down")

	if err := f.svc.Reply(context.Background(), testWS, f.threadID, "hello", nil); err != nil {
		t.Fatalf("Reply: %v, want nil (the reply is queued; a mark-read failure must not surface)", err)
	}
	if len(f.deps.enq.calls) != 1 {
		t.Fatalf("enqueue called %d times, want 1", len(f.deps.enq.calls))
	}
}

// The outstanding-sends cap now applies to the immediate path too, because it
// now writes the same rows the deferred path does. Without it, POST /reply would
// be an uncapped way around MaxOutstandingPendingSends.
func TestReplyIsSubjectToTheOutstandingSendCap(t *testing.T) {
	f := newReplyFixture(t)
	ctx := context.Background()
	for range inbox.MaxOutstandingPendingSends {
		if _, err := f.deps.pending.CreatePendingReply(ctx, inbox.CreatePendingReplyInput{
			WorkspaceID: testWS, ThreadID: f.threadID, BodyText: "queued", SendAfter: f.now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := f.svc.Reply(ctx, testWS, f.threadID, "one too many", nil); !errors.Is(err, inbox.ErrValidation) {
		t.Fatalf("Reply at capacity = %v, want ErrValidation", err)
	}
}

// Without a pending-reply store there is nowhere to put the body, so Reply must
// fail rather than silently drop it — unlike the optional SuppressionChecker,
// there is no safe "unwired" default for actually sending mail.
func TestReplyWithoutAPendingStoreFails(t *testing.T) {
	store := newFakeStore()
	threadID := seedThreadWithInbound(store, testWS, "lead@x.test")
	svc := inbox.NewService(store)

	if err := svc.Reply(context.Background(), testWS, threadID, "hello", nil); err == nil {
		t.Fatal("Reply with no pending-reply store wired = nil error, want an error")
	}
}
