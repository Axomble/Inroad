package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// fakePendingStore is an in-memory PendingReplyStore + PendingReplyClaimer that
// enforces the same status guards the SQL does. The guards ARE the feature — a
// fake that let any status transition to any other would make every test below
// meaningless — so each transition checks its expected prior state exactly as
// the corresponding query's WHERE clause does.
type fakePendingStore struct {
	rows map[uuid.UUID]inbox.PendingReply
	// now mirrors the Service's clock so send_after comparisons agree.
	now        func() time.Time
	undoWindow time.Duration
	createErr  error
	findDupErr error
	// threads models the INSERT … SELECT's self-enforcing tenancy: a row is
	// written only for a thread that exists IN THAT WORKSPACE. Reading the same
	// thread store the Service reads means a fixture cannot accidentally teach
	// this fake about a thread the Service has never heard of.
	threads *fakeStore
}

func newFakePendingStore(now func() time.Time, threads *fakeStore) *fakePendingStore {
	return &fakePendingStore{
		rows:       map[uuid.UUID]inbox.PendingReply{},
		now:        now,
		undoWindow: 10 * time.Second,
		threads:    threads,
	}
}

func (f *fakePendingStore) CreatePendingReply(_ context.Context, in inbox.CreatePendingReplyInput) (inbox.PendingReply, error) {
	if f.createErr != nil {
		return inbox.PendingReply{}, f.createErr
	}
	// The real INSERT … SELECT writes zero rows for a thread outside the
	// workspace, which surfaces as not-found.
	if th, ok := f.threads.threads[in.ThreadID]; !ok || th.WorkspaceID != in.WorkspaceID {
		return inbox.PendingReply{}, inbox.ErrNotFound
	}
	row := inbox.PendingReply{
		ID: uuid.New(), WorkspaceID: in.WorkspaceID, ThreadID: in.ThreadID,
		BodyText: in.BodyText, Status: inbox.PendingStatusScheduled,
		SendAfter: in.SendAfter, CreatedBy: in.CreatedBy, CreatedAt: f.now(),
	}
	f.rows[row.ID] = row
	return row, nil
}

func (f *fakePendingStore) GetPendingReply(_ context.Context, ws, id uuid.UUID) (inbox.PendingReply, error) {
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws {
		return inbox.PendingReply{}, inbox.ErrNotFound
	}
	return row, nil
}

func (f *fakePendingStore) ListPendingReplies(_ context.Context, ws uuid.UUID, limit int32) ([]inbox.PendingReply, error) {
	var out []inbox.PendingReply
	for _, row := range f.rows {
		if row.WorkspaceID != ws {
			continue
		}
		if row.Status != inbox.PendingStatusScheduled && row.Status != inbox.PendingStatusSending {
			continue
		}
		out = append(out, row)
		if int32(len(out)) >= limit {
			break
		}
	}
	return out, nil
}

// CountPendingReplies is a COUNT(*), not a counted page: the real query has no
// LIMIT, and reusing the paged list here would silently cap the count at the
// page size — which is well below MaxOutstandingPendingSends, so the capacity
// guard could never fire and every test of it would pass vacuously.
func (f *fakePendingStore) CountPendingReplies(_ context.Context, ws uuid.UUID) (int64, error) {
	var n int64
	for _, row := range f.rows {
		if row.WorkspaceID != ws {
			continue
		}
		if row.Status == inbox.PendingStatusScheduled || row.Status == inbox.PendingStatusSending {
			n++
		}
	}
	return n, nil
}

// FindDuplicatePendingReply mirrors the real query's discriminators — same
// workspace, same thread, same BODY, still 'scheduled', inside the window — rather
// than just answering "a row exists". A fake that ignored the body would make the
// dedup tests pass while the production guard refused legitimate second replies.
func (f *fakePendingStore) FindDuplicatePendingReply(_ context.Context, ws, threadID uuid.UUID, bodyText string, within time.Duration) (uuid.UUID, error) {
	if f.findDupErr != nil {
		return uuid.Nil, f.findDupErr
	}
	cutoff := f.now().Add(-within)
	for _, row := range f.rows {
		if row.WorkspaceID != ws || row.ThreadID != threadID || row.BodyText != bodyText {
			continue
		}
		if row.Status == inbox.PendingStatusScheduled && row.CreatedAt.After(cutoff) {
			return row.ID, nil
		}
	}
	return uuid.Nil, nil
}

func (f *fakePendingStore) PendingReplyForThread(_ context.Context, ws, threadID uuid.UUID) (inbox.PendingReply, error) {
	for _, row := range f.rows {
		if row.WorkspaceID != ws || row.ThreadID != threadID {
			continue
		}
		if row.Status == inbox.PendingStatusScheduled || row.Status == inbox.PendingStatusSending {
			return row, nil
		}
	}
	return inbox.PendingReply{}, inbox.ErrNotFound
}

// CancelPendingReply mirrors the SQL's `AND status = 'scheduled'` guard: a row
// already claimed is past the point of no return.
func (f *fakePendingStore) CancelPendingReply(_ context.Context, ws, id uuid.UUID) error {
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws || row.Status != inbox.PendingStatusScheduled {
		return inbox.ErrNotFound
	}
	row.Status = inbox.PendingStatusCancelled
	f.rows[id] = row
	return nil
}

func (f *fakePendingStore) UndoWindow(context.Context, uuid.UUID) (time.Duration, error) {
	return f.undoWindow, nil
}

func (f *fakePendingStore) SetUndoWindow(_ context.Context, _ uuid.UUID, seconds int32) error {
	f.undoWindow = time.Duration(seconds) * time.Second
	return nil
}

// ClaimPendingReply mirrors the SQL: due, and either scheduled or a lease-
// expired 'sending'.
func (f *fakePendingStore) ClaimPendingReply(_ context.Context, ws, id uuid.UUID) error {
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws {
		return inbox.ErrPendingNotClaimable
	}
	if row.SendAfter.After(f.now()) {
		return inbox.ErrPendingNotClaimable
	}
	if row.Status != inbox.PendingStatusScheduled {
		return inbox.ErrPendingNotClaimable
	}
	row.Status = inbox.PendingStatusSending
	f.rows[id] = row
	return nil
}

func (f *fakePendingStore) MarkPendingReplySent(_ context.Context, ws, id uuid.UUID, messageID string) error {
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws || row.Status != inbox.PendingStatusSending {
		return inbox.ErrNotFound
	}
	sent := f.now()
	row.Status, row.MessageID, row.SentAt = inbox.PendingStatusSent, messageID, &sent
	f.rows[id] = row
	return nil
}

func (f *fakePendingStore) ReleasePendingReply(_ context.Context, ws, id uuid.UUID, reason string) error {
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws || row.Status != inbox.PendingStatusSending {
		return nil
	}
	row.Status, row.LastError = inbox.PendingStatusScheduled, reason
	f.rows[id] = row
	return nil
}

func (f *fakePendingStore) FailPendingReply(_ context.Context, ws, id uuid.UUID, reason string) error {
	row, ok := f.rows[id]
	if !ok || row.WorkspaceID != ws {
		return nil
	}
	row.Status, row.LastError = inbox.PendingStatusFailed, reason
	f.rows[id] = row
	return nil
}

// recordingPendingEnqueuer captures what was scheduled, so a test can assert
// the delay actually reached the queue.
type recordingPendingEnqueuer struct {
	calls []pendingEnqueueCall
	err   error
}

// pendingEnqueueCall is the WHOLE payload the queue is handed: two ids and an
// instant. There is deliberately no body field to record, because the interface
// has no body argument — that absence is the fix this package's tests exist to
// hold in place.
type pendingEnqueueCall struct {
	pendingID   string
	workspaceID string
	sendAfter   time.Time
}

func (r *recordingPendingEnqueuer) EnqueuePendingInboxReply(pendingID, workspaceID string, sendAfter time.Time) error {
	if r.err != nil {
		return r.err
	}
	r.calls = append(r.calls, pendingEnqueueCall{pendingID, workspaceID, sendAfter})
	return nil
}

type pendingFixture struct {
	svc     *inbox.Service
	handler *inbox.Handler
	threads *fakeStore
	pending *fakePendingStore
	enq     *recordingPendingEnqueuer
	now     time.Time
	thread  inbox.Thread
}

func newPendingFixture(t *testing.T) *pendingFixture {
	t.Helper()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	threads := newFakeStore()
	pending := newFakePendingStore(clock, threads)
	enq := &recordingPendingEnqueuer{}

	svc := inbox.NewService(threads,
		inbox.WithPendingReplyStore(pending),
		inbox.WithPendingReplyEnqueuer(enq),
		inbox.WithClock(clock),
	)

	// A thread with an inbound message, which ScheduleReply requires.
	th, err := threads.RecordReply(context.Background(), inbox.UpsertThreadInput{
		WorkspaceID: testWS, MailboxID: uuid.New(), RootMessageID: "<pending@s.test>", Subject: "S",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", BodyText: "hi", OccurredAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	return &pendingFixture{
		svc: svc, handler: inbox.NewHandler(svc),
		threads: threads, pending: pending, enq: enq, now: now, thread: th,
	}
}

func TestScheduleReplyAppliesTheUndoWindow(t *testing.T) {
	f := newPendingFixture(t)
	f.pending.undoWindow = 30 * time.Second

	got, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "on it", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	want := f.now.Add(30 * time.Second)
	if !got.SendAfter.Equal(want) {
		t.Errorf("SendAfter = %v, want %v", got.SendAfter, want)
	}
	if got.Status != inbox.PendingStatusScheduled {
		t.Errorf("Status = %q, want scheduled", got.Status)
	}
	if !got.Cancellable() {
		t.Error("a freshly scheduled reply is not cancellable")
	}
}

// A zero window means send immediately — the workspace opted out of undo — and
// "immediately" has to mean STRICTLY IN THE PAST, not "now".
//
// ClaimInboxPendingReply guards on `send_after <= now()` evaluated on the
// DATABASE clock (queries/inbox.sql); this instant is stamped from the APP
// clock. An equal instant therefore loses to any forward skew between the two:
// the task fires, the claim matches nothing, and — there being no sweeper over
// stranded 'scheduled' rows — the reply never leaves while the operator is told
// it is on its way.
func TestScheduleReplyWithNoUndoWindowIsDueStrictlyBeforeNow(t *testing.T) {
	f := newPendingFixture(t)
	f.pending.undoWindow = 0

	got, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "on it", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	if !got.SendAfter.Before(f.now) {
		t.Errorf("SendAfter = %v, want strictly before now (%v)", got.SendAfter, f.now)
	}
	// The point of being in the past, asserted rather than described: the row is
	// claimable the moment its task fires.
	if err := f.svc.ClaimPendingReply(context.Background(), testWS, got.ID); err != nil {
		t.Errorf("ClaimPendingReply on a zero-window row: %v, want it claimable at once", err)
	}
}

// A NON-zero window still lands exactly on now+window: the skew slack is for the
// already-due case only, and must never pull a genuinely scheduled send early.
func TestScheduleReplyDoesNotApplySkewSlackToAScheduledSend(t *testing.T) {
	f := newPendingFixture(t)
	f.pending.undoWindow = 30 * time.Second

	got, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "on it", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	if !got.SendAfter.Equal(f.now.Add(30 * time.Second)) {
		t.Errorf("SendAfter = %v, want exactly now+30s (%v): the undo window is a promise to the "+
			"operator, so nothing may shorten it", got.SendAfter, f.now.Add(30*time.Second))
	}
}

func TestScheduleReplyHonoursAnExplicitInstant(t *testing.T) {
	f := newPendingFixture(t)
	at := f.now.Add(6 * time.Hour)

	got, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "later", &at, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	if !got.SendAfter.Equal(at) {
		t.Errorf("SendAfter = %v, want %v", got.SendAfter, at)
	}
}

func TestScheduleReplyRejectsOutOfRangeInstants(t *testing.T) {
	tests := []struct {
		name string
		at   time.Duration
		want error
	}{
		{"in the past", -time.Hour, inbox.ErrScheduleInPast},
		{"right now", 0, inbox.ErrScheduleInPast},
		{"beyond the horizon", inbox.MaxScheduleHorizon + time.Minute, inbox.ErrScheduleTooFar},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newPendingFixture(t)
			at := f.now.Add(tc.at)
			_, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "x", &at, nil)
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}

	// Exactly at the horizon is allowed — the bound is inclusive.
	f := newPendingFixture(t)
	at := f.now.Add(inbox.MaxScheduleHorizon)
	if _, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "x", &at, nil); err != nil {
		t.Errorf("a send exactly at the horizon was rejected: %v", err)
	}
}

// The queue must be told the SAME moment the row holds, or the task fires at a
// time that disagrees with the authority.
func TestScheduleReplyEnqueuesForTheRowsSendAfter(t *testing.T) {
	f := newPendingFixture(t)
	got, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "on it", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	if len(f.enq.calls) != 1 {
		t.Fatalf("%d enqueue calls, want 1", len(f.enq.calls))
	}
	call := f.enq.calls[0]
	if call.pendingID != got.ID.String() {
		t.Errorf("enqueued %s, want %s", call.pendingID, got.ID)
	}
	if !call.sendAfter.Equal(got.SendAfter) {
		t.Errorf("enqueued for %v, but the row says %v", call.sendAfter, got.SendAfter)
	}
}

// An enqueue failure must be REPORTED, not swallowed. There is no sweeper yet,
// so a silent failure would leave a row nothing ever picks up while telling the
// operator their reply was queued.
func TestScheduleReplyReportsAnEnqueueFailure(t *testing.T) {
	f := newPendingFixture(t)
	f.enq.err = errors.New("redis down")

	if _, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "on it", nil, nil); err == nil {
		t.Fatal("ScheduleReply reported success despite an enqueue failure")
	}
}

func TestScheduleReplyValidatesLikeTheImmediatePath(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()

	if _, err := f.svc.ScheduleReply(ctx, testWS, f.thread.ID, "", nil, nil); !errors.Is(err, inbox.ErrReplyBodyInvalid) {
		t.Errorf("empty body: error = %v, want ErrReplyBodyInvalid", err)
	}
	if _, err := f.svc.ScheduleReply(ctx, testWS, uuid.New(), "x", nil, nil); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("unknown thread: error = %v, want ErrNotFound", err)
	}

	// A thread with no inbound message has nothing to reply to.
	bare, err := f.threads.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: testWS, MailboxID: uuid.New(), RootMessageID: "<bare@s.test>",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := f.svc.ScheduleReply(ctx, testWS, bare.ID, "x", nil, nil); !errors.Is(err, inbox.ErrNoInboundMessage) {
		t.Errorf("no inbound: error = %v, want ErrNoInboundMessage", err)
	}
}

// A scheduled reply must NOT mark the thread read: an undone send should leave
// the thread exactly as the operator found it.
func TestScheduleReplyLeavesTheThreadUnread(t *testing.T) {
	f := newPendingFixture(t)
	if _, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "on it", nil, nil); err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	detail, err := f.svc.GetThread(context.Background(), testWS, f.thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if !detail.Thread.Unread {
		t.Error("scheduling a reply marked the thread read; an undo would then have lost that state")
	}
}

func TestCancelPendingReplyUndoesIt(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleReply(ctx, testWS, f.thread.ID, "oops", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}

	if err := f.svc.CancelPendingReply(ctx, testWS, got.ID); err != nil {
		t.Fatalf("CancelPendingReply: %v", err)
	}
	after, err := f.svc.GetPendingReply(ctx, testWS, got.ID)
	if err != nil {
		t.Fatalf("GetPendingReply: %v", err)
	}
	if after.Status != inbox.PendingStatusCancelled {
		t.Errorf("Status = %q, want cancelled", after.Status)
	}
}

// The distinction that earns a second query: a row past 'scheduled' is not
// "missing", and telling the operator it was would be a lie.
func TestCancelPendingReplyAfterClaimingSaysWhy(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleReply(ctx, testWS, f.thread.ID, "too late", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	// The worker claims it (time has moved past send_after in the real world;
	// here the fake is driven directly).
	row := f.pending.rows[got.ID]
	row.Status = inbox.PendingStatusSending
	f.pending.rows[got.ID] = row

	err = f.svc.CancelPendingReply(ctx, testWS, got.ID)
	if !errors.Is(err, inbox.ErrPendingNotCancellable) {
		t.Errorf("error = %v, want ErrPendingNotCancellable", err)
	}
}

func TestCancelAnUnknownPendingReplyIsNotFound(t *testing.T) {
	f := newPendingFixture(t)
	err := f.svc.CancelPendingReply(context.Background(), testWS, uuid.New())
	if !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// Invariant: another workspace can neither read nor cancel this one's send.
func TestPendingReplyIsWorkspaceScoped(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleReply(ctx, testWS, f.thread.ID, "mine", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	foreign := uuid.New()

	if _, err := f.svc.GetPendingReply(ctx, foreign, got.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("GetPendingReply(foreign) = %v, want ErrNotFound", err)
	}
	if err := f.svc.CancelPendingReply(ctx, foreign, got.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("CancelPendingReply(foreign) = %v, want ErrNotFound", err)
	}
	// ...and it survived the attempt, still cancellable by its owner.
	still, err := f.svc.GetPendingReply(ctx, testWS, got.ID)
	if err != nil || still.Status != inbox.PendingStatusScheduled {
		t.Errorf("the owner's reply was disturbed: %+v (%v)", still, err)
	}
}

// A thread with a reply in flight reports it, so the reader can show the
// countdown and Undo.
func TestGetThreadCarriesTheInFlightReply(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleReply(ctx, testWS, f.thread.ID, "on it", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}

	detail, err := f.svc.GetThread(ctx, testWS, f.thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if detail.PendingReply == nil || detail.PendingReply.ID != got.ID {
		t.Fatalf("PendingReply = %+v, want %s", detail.PendingReply, got.ID)
	}

	// Once cancelled it is no longer in flight, so the reader stops showing it.
	if err := f.svc.CancelPendingReply(ctx, testWS, got.ID); err != nil {
		t.Fatalf("CancelPendingReply: %v", err)
	}
	detail, err = f.svc.GetThread(ctx, testWS, f.thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if detail.PendingReply != nil {
		t.Errorf("PendingReply = %+v, want nil after cancelling", detail.PendingReply)
	}
}

// The claim's due-time guard: a task that fires early must not send.
func TestClaimBeforeSendAfterIsRefused(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	at := f.now.Add(time.Hour)
	got, err := f.svc.ScheduleReply(ctx, testWS, f.thread.ID, "later", &at, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}

	if err := f.svc.ClaimPendingReply(ctx, testWS, got.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Errorf("claiming an undue reply = %v, want ErrPendingNotClaimable", err)
	}
}

// A cancelled reply must never be claimable — this is what makes undo real
// rather than a race the worker might win.
func TestACancelledReplyIsNeverClaimable(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleReply(ctx, testWS, f.thread.ID, "oops", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	if err := f.svc.CancelPendingReply(ctx, testWS, got.ID); err != nil {
		t.Fatalf("CancelPendingReply: %v", err)
	}

	if err := f.svc.ClaimPendingReply(ctx, testWS, got.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Errorf("a cancelled reply was claimable: %v", err)
	}
}

// Only one worker may claim: the second must be refused, or the reply goes out
// twice.
func TestOnlyOneWorkerCanClaim(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	at := f.now.Add(-time.Minute) // already due
	got, err := f.pending.CreatePendingReply(ctx, inbox.CreatePendingReplyInput{
		WorkspaceID: testWS, ThreadID: f.thread.ID, BodyText: "go", SendAfter: at,
	})
	if err != nil {
		t.Fatalf("CreatePendingReply: %v", err)
	}

	if err := f.svc.ClaimPendingReply(ctx, testWS, got.ID); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := f.svc.ClaimPendingReply(ctx, testWS, got.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Errorf("second claim = %v, want ErrPendingNotClaimable", err)
	}
}

func TestMarkSentRequiresTheClaim(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleReply(ctx, testWS, f.thread.ID, "go", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}

	// Still 'scheduled' — completing it without claiming must fail.
	if err := f.svc.MarkPendingReplySent(ctx, testWS, got.ID, "<mid@x>"); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("MarkPendingReplySent without a claim = %v, want ErrNotFound", err)
	}
}

func TestSetUndoWindowBounds(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()

	for _, seconds := range []int32{-1, inbox.MaxUndoSendSeconds + 1} {
		if err := f.svc.SetUndoWindow(ctx, testWS, seconds); !errors.Is(err, inbox.ErrValidation) {
			t.Errorf("SetUndoWindow(%d) = %v, want ErrValidation", seconds, err)
		}
	}
	for _, seconds := range []int32{0, 30, inbox.MaxUndoSendSeconds} {
		if err := f.svc.SetUndoWindow(ctx, testWS, seconds); err != nil {
			t.Errorf("SetUndoWindow(%d): %v", seconds, err)
		}
	}
}

// --- HTTP layer ---

func TestScheduleReplyEndpointReturnsTheHandle(t *testing.T) {
	f := newPendingFixture(t)
	res := serve(t, f.handler, http.MethodPost,
		"/inbox/threads/"+f.thread.ID.String()+"/schedule-reply", `{"body_text":"on it"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", res.Code, res.Body.String())
	}
	var got struct {
		ID          string `json:"id"`
		SendAfter   string `json:"send_after"`
		Status      string `json:"status"`
		Cancellable bool   `json:"cancellable"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The id IS the feature: without it the client has no undo handle.
	if got.ID == "" {
		t.Error("no id returned — there is nothing to undo with")
	}
	if got.SendAfter == "" {
		t.Error("no send_after returned — the client cannot run a countdown")
	}
	if !got.Cancellable {
		t.Error("cancellable = false on a freshly scheduled reply")
	}
}

func TestScheduleReplyEndpointStatusCodes(t *testing.T) {
	f := newPendingFixture(t)
	far := f.now.Add(inbox.MaxScheduleHorizon + time.Hour).Format(time.RFC3339)
	past := f.now.Add(-time.Hour).Format(time.RFC3339)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"unknown field", `{"body_text":"x","nope":1}`, http.StatusBadRequest},
		{"empty body", `{"body_text":""}`, http.StatusUnprocessableEntity},
		{"bad timestamp", `{"body_text":"x","send_at":"soon"}`, http.StatusBadRequest},
		{"past instant", `{"body_text":"x","send_at":"` + past + `"}`, http.StatusUnprocessableEntity},
		{"beyond the horizon", `{"body_text":"x","send_at":"` + far + `"}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := serve(t, f.handler, http.MethodPost,
				"/inbox/threads/"+f.thread.ID.String()+"/schedule-reply", tc.body)
			if res.Code != tc.want {
				t.Errorf("status = %d, want %d (%s)", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

func TestOutboxEndpointListsAndCancels(t *testing.T) {
	f := newPendingFixture(t)
	created := serve(t, f.handler, http.MethodPost,
		"/inbox/threads/"+f.thread.ID.String()+"/schedule-reply", `{"body_text":"on it"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("schedule status = %d (%s)", created.Code, created.Body.String())
	}
	var scheduled struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &scheduled); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	listed := serve(t, f.handler, http.MethodGet, "/inbox/outbox", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s)", listed.Code, listed.Body.String())
	}
	var page struct {
		Items []struct{ ID string } `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != scheduled.ID {
		t.Fatalf("outbox = %+v, want the one scheduled reply", page.Items)
	}

	cancelled := serve(t, f.handler, http.MethodDelete, "/inbox/outbox/"+scheduled.ID, "")
	if cancelled.Code != http.StatusNoContent {
		t.Errorf("cancel status = %d, want 204 (%s)", cancelled.Code, cancelled.Body.String())
	}

	// The outbox is now empty — a cancelled reply is not waiting.
	after := serve(t, f.handler, http.MethodGet, "/inbox/outbox", "")
	var emptyPage struct {
		Items []struct{ ID string } `json:"items"`
	}
	if err := json.Unmarshal(after.Body.Bytes(), &emptyPage); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(emptyPage.Items) != 0 {
		t.Errorf("outbox still lists %d items after cancelling", len(emptyPage.Items))
	}
}

// THE CAP'S STATUS CODE, asserted at the HANDLER on BOTH send routes.
//
// The service-level test (TestReplyIsSubjectToTheOutstandingSendCap) proves the
// cap fires and names its sentinel; it says nothing about what a client sees,
// which is how the cap shipped documented as 422 and answering 400. A
// workspace-state rejection of a well-formed request is 422 — the same class as
// ErrTooManyLabels and the snooze bounds — and the SPA branches on the status,
// so the two routes must agree with each other and with the OpenAPI contract.
func TestBothSendRoutesReturn422AtTheOutstandingSendCap(t *testing.T) {
	f := newPendingFixture(t)
	ctx := context.Background()
	for range inbox.MaxOutstandingPendingSends {
		if _, err := f.pending.CreatePendingReply(ctx, inbox.CreatePendingReplyInput{
			WorkspaceID: testWS, ThreadID: f.thread.ID, BodyText: "queued",
			SendAfter: f.now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	for _, tc := range []struct{ name, path, body string }{
		{"immediate", "/reply", `{"body_text":"one too many"}`},
		{"deferred", "/schedule-reply", `{"body_text":"one too many"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := serve(t, f.handler, http.MethodPost,
				"/inbox/threads/"+f.thread.ID.String()+tc.path, tc.body)
			if res.Code != http.StatusUnprocessableEntity {
				t.Errorf("POST %s at capacity = %d, want 422 (%s)", tc.path, res.Code, res.Body.String())
			}
		})
	}
}

// 409, not 404: the reply exists and the operator needs told why the click
// failed.
func TestCancelEndpointConflictsOnceSending(t *testing.T) {
	f := newPendingFixture(t)
	got, err := f.svc.ScheduleReply(context.Background(), testWS, f.thread.ID, "too late", nil, nil)
	if err != nil {
		t.Fatalf("ScheduleReply: %v", err)
	}
	row := f.pending.rows[got.ID]
	row.Status = inbox.PendingStatusSending
	f.pending.rows[got.ID] = row

	res := serve(t, f.handler, http.MethodDelete, "/inbox/outbox/"+got.ID.String(), "")
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (%s)", res.Code, res.Body.String())
	}
}

func TestInboxSettingsEndpointRoundTrip(t *testing.T) {
	f := newPendingFixture(t)

	res := serve(t, f.handler, http.MethodPut, "/inbox/settings", `{"undo_send_seconds":45}`)
	if res.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", res.Code, res.Body.String())
	}

	res = serve(t, f.handler, http.MethodGet, "/inbox/settings", "")
	if res.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", res.Code)
	}
	var got struct {
		UndoSendSeconds int `json:"undo_send_seconds"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.UndoSendSeconds != 45 {
		t.Errorf("undo_send_seconds = %d, want 45", got.UndoSendSeconds)
	}

	// Out of range is a 400 (ErrValidation), not silently clamped.
	res = serve(t, f.handler, http.MethodPut, "/inbox/settings", `{"undo_send_seconds":9999}`)
	if res.Code != http.StatusBadRequest {
		t.Errorf("out-of-range status = %d, want 400 (%s)", res.Code, res.Body.String())
	}
}
