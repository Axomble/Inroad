package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
)

// No database anywhere in this file, for the reason the rest of the package's
// tests state: the thing under test is a client that HAS no database access.
// The end-to-end proof against real Postgres — through the real handler, from a
// worker holding NO POOL AT ALL — lives in
// internal/coreapi/inprocess/remoteinbound_integration_test.go.

// fakeInbound is the CONTROL plane's side of the inbound-mail path: it records
// what it was asked and returns fixed answers.
type fakeInbound struct {
	calls   int
	gotArgs []string
	// got is the last INPUT struct a route handed the implementation, kept as
	// `any` so one field serves all four input-carrying routes. Tests type-assert
	// it; a test that asserts the wrong type fails loudly rather than reading a
	// zero value out of a second typed field nobody set.
	got     any
	err     error
	plan    coreapi.WarmupEngagePlan
	label   coreapi.ReplyLabel
	found   bool
	send    coreapi.WarmupSendRef
	matched bool
	backoff coreapi.InboxPollBackoff
}

func (f *fakeInbound) record(args ...string) { f.calls++; f.gotArgs = args }

func (f *fakeInbound) SetInboxCursor(_ context.Context, mailboxID, workspaceID string, lastSeenUID, uidValidity uint32) error {
	f.record(mailboxID, workspaceID)
	f.got = [2]uint32{lastSeenUID, uidValidity}
	return f.err
}

func (f *fakeInbound) SetInboxCursorString(_ context.Context, mailboxID, workspaceID, cursor string) error {
	f.record(mailboxID, workspaceID, cursor)
	return f.err
}

func (f *fakeInbound) RecordInboxPollFailure(_ context.Context, mailboxID, workspaceID string, ladder []time.Duration) (coreapi.InboxPollBackoff, error) {
	f.record(mailboxID, workspaceID)
	f.got = ladder
	return f.backoff, f.err
}

func (f *fakeInbound) StoreInboundMessage(_ context.Context, in coreapi.InboxMessageInput) error {
	f.record(in.WorkspaceID, in.MailboxID)
	f.got = in
	return f.err
}

func (f *fakeInbound) CaptureCRMReply(_ context.Context, in coreapi.CRMReplyInput) error {
	f.record(in.WorkspaceID, in.SendID)
	f.got = in
	return f.err
}

func (f *fakeInbound) IngestComplaint(_ context.Context, in coreapi.ComplaintInput) error {
	f.record(in.WorkspaceID, in.SendID)
	f.got = in
	return f.err
}

func (f *fakeInbound) ResolveReplyLabel(_ context.Context, workspaceID, key string) (coreapi.ReplyLabel, bool, error) {
	f.record(workspaceID, key)
	return f.label, f.found, f.err
}

func (f *fakeInbound) RecordWarmupReceipt(_ context.Context, in coreapi.WarmupReceiptInput) (coreapi.WarmupEngagePlan, error) {
	f.record(in.WorkspaceID, in.WarmupSendID, in.RecipientMailbox)
	f.got = in
	return f.plan, f.err
}

func (f *fakeInbound) FindWarmupSendByMessageID(_ context.Context, workspaceID, toMailboxID, messageID string) (coreapi.WarmupSendRef, bool, error) {
	f.record(workspaceID, toMailboxID, messageID)
	return f.send, f.found, f.err
}

func (f *fakeInbound) RecordWarmupTokenFailure(_ context.Context, workspaceID, recipientMailbox, fingerprint, reasonCode string) error {
	f.record(workspaceID, recipientMailbox, fingerprint, reasonCode)
	return f.err
}

func (f *fakeInbound) RecordWarmupHardBounce(_ context.Context, workspaceID, messageID, observerMailbox string) (bool, error) {
	f.record(workspaceID, messageID, observerMailbox)
	return f.matched, f.err
}

// fakeFleet is the control plane's side of the worker-infrastructure path.
type fakeFleet struct {
	calls   int
	gotArgs []string
	got     any
	err     error
	queue   string
}

func (f *fakeFleet) record(args ...string) { f.calls++; f.gotArgs = args }

func (f *fakeFleet) UpsertWorkerHeartbeat(_ context.Context, workerID, egressIP, idFamily string) error {
	f.record(workerID, egressIP, idFamily)
	return f.err
}

func (f *fakeFleet) RecordWorkerProviderSignals(_ context.Context, in coreapi.WorkerProviderSignals) error {
	f.record(in.WorkerID)
	f.got = in
	return f.err
}

func (f *fakeFleet) AssignMailboxWorker(_ context.Context, mailboxID, workspaceID string) (string, error) {
	f.record(mailboxID, workspaceID)
	return f.queue, f.err
}

func (f *fakeFleet) RecordDeadLetter(_ context.Context, in coreapi.DeadLetterInput) error {
	f.record(in.WorkspaceID, in.TaskType)
	f.got = in
	return f.err
}

// serveSlice4 stands the real handler up over the two fake writers and returns
// a client pointed at it.
func serveSlice4(t *testing.T, inbound *fakeInbound, fl *fakeFleet, jobs *fakeJobs) (*Client, *httptest.Server) {
	t.Helper()
	h, err := NewHandler(Deps{
		Suppression: &fakeSuppression{}, Jobs: jobs, Outcomes: &fakeOutcomes{},
		InboxSends: &fakeInboxSends{}, Inbound: inbound, Fleet: fl,
	}, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, testToken, true, &fakeOpener{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

// Every slice-4 method reaches the control plane and comes back with the
// control plane's answer — the baseline the failure cases below are measured
// against, and the thing a reader most wants to know: these fifteen calls work
// over the wire.
func TestEverySlice4MethodRoundTrips(t *testing.T) {
	ws, mailbox, send := uuid.NewString(), uuid.NewString(), uuid.NewString()
	enrollment, receipt := uuid.NewString(), uuid.NewString()

	inbound := &fakeInbound{
		found:   true,
		matched: true,
		plan:    coreapi.WarmupEngagePlan{ReceiptID: receipt, DoMarkRead: true, EngageAfter: 90 * time.Second},
		label:   coreapi.ReplyLabel{Key: "positive", StopsEnrollment: true},
		send:    coreapi.WarmupSendRef{WarmupSendID: send},
		backoff: coreapi.InboxPollBackoff{
			Failures: 3, RetryAfter: time.Now().Add(12 * time.Minute).UTC().Truncate(time.Millisecond),
		},
	}
	fl := &fakeFleet{queue: "w:box-1"}
	jobs := &fakeJobs{due: time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond), sendNow: true}
	c, _ := serveSlice4(t, inbound, fl, jobs)
	ctx := t.Context()

	t.Run("SetInboxCursor", func(t *testing.T) {
		if err := c.SetInboxCursor(ctx, mailbox, ws, 4242, 99); err != nil {
			t.Fatalf("SetInboxCursor: %v", err)
		}
		if got, want := inbound.got, [2]uint32{4242, 99}; got != want {
			t.Errorf("cursor = %v, want %v", got, want)
		}
	})

	t.Run("SetInboxCursorString", func(t *testing.T) {
		if err := c.SetInboxCursorString(ctx, mailbox, ws, "history:1234"); err != nil {
			t.Fatalf("SetInboxCursorString: %v", err)
		}
		if got := inbound.gotArgs[2]; got != "history:1234" {
			t.Errorf("cursor = %q, want %q", got, "history:1234")
		}
	})

	t.Run("RecordInboxPollFailure", func(t *testing.T) {
		ladder := []time.Duration{3 * time.Minute, 6 * time.Minute, time.Hour}
		out, err := c.RecordInboxPollFailure(ctx, mailbox, ws, ladder)
		if err != nil {
			t.Fatalf("RecordInboxPollFailure: %v", err)
		}
		// The ladder crosses as SECONDS, so a transport that lost the unit would
		// hand the control plane 180 nanoseconds and back a failing mailbox off
		// by nothing at all.
		got, ok := inbound.got.([]time.Duration)
		if !ok {
			t.Fatalf("handler got %T, want []time.Duration", inbound.got)
		}
		if len(got) != len(ladder) || got[0] != 3*time.Minute || got[2] != time.Hour {
			t.Errorf("ladder = %v, want %v", got, ladder)
		}
		if out.Failures != inbound.backoff.Failures || !out.RetryAfter.Equal(inbound.backoff.RetryAfter) {
			t.Errorf("backoff = %+v, want %+v", out, inbound.backoff)
		}
	})

	t.Run("RecordInboxPollFailure refuses a ladder that could not bound a retry", func(t *testing.T) {
		before := inbound.calls
		for _, bad := range [][]time.Duration{nil, {}, {3 * time.Minute, 0}, {-time.Minute}} {
			if _, err := c.RecordInboxPollFailure(ctx, mailbox, ws, bad); !errors.Is(err, coreapi.ErrInvalidBackoffLadder) {
				t.Errorf("ladder %v: err = %v, want ErrInvalidBackoffLadder", bad, err)
			}
		}
		if inbound.calls != before {
			t.Error("an invalid ladder reached the control plane")
		}
	})

	t.Run("StoreInboundMessage", func(t *testing.T) {
		campaign := uuid.NewString()
		in := coreapi.InboxMessageInput{
			WorkspaceID: ws, MailboxID: mailbox, CampaignID: &campaign,
			Subject: "Re: hello", BodyText: "sure, let's talk", MessageID: "<a@b>",
			OccurredAt: time.Now().UTC().Truncate(time.Second),
		}
		if err := c.StoreInboundMessage(ctx, in); err != nil {
			t.Fatalf("StoreInboundMessage: %v", err)
		}
		got, ok := inbound.got.(coreapi.InboxMessageInput)
		if !ok {
			t.Fatalf("handler got %T, want coreapi.InboxMessageInput", inbound.got)
		}
		// The nullable ids are the reason the whole input travels rather than a
		// mirror: a forgotten *string arrives nil and files the reply under no
		// campaign.
		if got.CampaignID == nil || *got.CampaignID != campaign {
			t.Errorf("campaign id = %v, want %q", got.CampaignID, campaign)
		}
		if got.BodyText != in.BodyText || got.Subject != in.Subject {
			t.Errorf("message body/subject did not survive: %+v", got)
		}
	})

	t.Run("CaptureCRMReply", func(t *testing.T) {
		in := coreapi.CRMReplyInput{
			WorkspaceID: ws, EnrollmentID: enrollment, SendID: send,
			Subject: "Re: hello", SenderEmail: "them@example.com", ReplyClass: "positive",
		}
		if err := c.CaptureCRMReply(ctx, in); err != nil {
			t.Fatalf("CaptureCRMReply: %v", err)
		}
		if got := inbound.got.(coreapi.CRMReplyInput); got.ReplyClass != "positive" {
			t.Errorf("reply class = %q, want positive", got.ReplyClass)
		}
	})

	t.Run("IngestComplaint", func(t *testing.T) {
		in := coreapi.ComplaintInput{
			WorkspaceID: ws, SendID: send, Email: "them@example.com", ProviderEventID: "arf:" + send,
		}
		if err := c.IngestComplaint(ctx, in); err != nil {
			t.Fatalf("IngestComplaint: %v", err)
		}
		if got := inbound.got.(coreapi.ComplaintInput); got.ProviderEventID != "arf:"+send {
			t.Errorf("provider event id = %q, want the namespaced one", got.ProviderEventID)
		}
	})

	t.Run("ResolveReplyLabel", func(t *testing.T) {
		label, ok, err := c.ResolveReplyLabel(ctx, ws, "positive")
		if err != nil {
			t.Fatalf("ResolveReplyLabel: %v", err)
		}
		if !ok || label.Key != "positive" || !label.StopsEnrollment {
			t.Errorf("label = %+v, ok = %v, want the stopping positive label", label, ok)
		}
	})

	t.Run("RecordWarmupReceipt", func(t *testing.T) {
		in := coreapi.WarmupReceiptInput{
			WorkspaceID: ws, WarmupSendID: send, RecipientMailbox: mailbox,
			Placement: "inbox", SourceFolder: "INBOX", MessageID: "<w@b>",
		}
		plan, err := c.RecordWarmupReceipt(ctx, in)
		if err != nil {
			t.Fatalf("RecordWarmupReceipt: %v", err)
		}
		// EngageAfter is a time.Duration and crosses as nanoseconds; a
		// transport that lost the unit would read 90 here instead of 90s.
		if plan.ReceiptID != receipt || !plan.DoMarkRead || plan.EngageAfter != 90*time.Second {
			t.Errorf("plan = %+v, want the control plane's plan with a 90s dwell", plan)
		}
	})

	t.Run("FindWarmupSendByMessageID", func(t *testing.T) {
		ref, ok, err := c.FindWarmupSendByMessageID(ctx, ws, mailbox, "<w@b>")
		if err != nil {
			t.Fatalf("FindWarmupSendByMessageID: %v", err)
		}
		if !ok || ref.WarmupSendID != send {
			t.Errorf("ref = %+v, ok = %v, want the warmup send", ref, ok)
		}
	})

	t.Run("RecordWarmupTokenFailure", func(t *testing.T) {
		if err := c.RecordWarmupTokenFailure(ctx, ws, mailbox, "ab12", "bad_signature"); err != nil {
			t.Fatalf("RecordWarmupTokenFailure: %v", err)
		}
		if got := inbound.gotArgs[3]; got != "bad_signature" {
			t.Errorf("reason code = %q, want bad_signature", got)
		}
	})

	t.Run("RecordWarmupHardBounce", func(t *testing.T) {
		matched, err := c.RecordWarmupHardBounce(ctx, ws, "<w@b>", mailbox)
		if err != nil {
			t.Fatalf("RecordWarmupHardBounce: %v", err)
		}
		if !matched {
			t.Error("matched = false, want the control plane's true")
		}
	})

	t.Run("NextWarmupDue", func(t *testing.T) {
		due, sendNow, err := c.NextWarmupDue(ctx, mailbox, ws)
		if err != nil {
			t.Fatalf("NextWarmupDue: %v", err)
		}
		if !due.Equal(jobs.due) || !sendNow {
			t.Errorf("due = %v sendNow = %v, want %v true", due, sendNow, jobs.due)
		}
	})

	t.Run("UpsertWorkerHeartbeat", func(t *testing.T) {
		// A worker id is NOT a uuid — a hostname here proves the transport does
		// not validate it as one, which would drop every host whose identity
		// came from the hostname fallback.
		if err := c.UpsertWorkerHeartbeat(ctx, "box-1.fleet.internal", "203.0.113.7", "hostname"); err != nil {
			t.Fatalf("UpsertWorkerHeartbeat: %v", err)
		}
		if got := fl.gotArgs[0]; got != "box-1.fleet.internal" {
			t.Errorf("worker id = %q, want the hostname", got)
		}
	})

	t.Run("RecordWorkerProviderSignals", func(t *testing.T) {
		in := coreapi.WorkerProviderSignals{
			WorkerID: "box-1", WindowStart: time.Now().Add(-time.Minute).UTC(), WindowEnd: time.Now().UTC(),
			Counts: []coreapi.WorkerProviderSignalCount{{Provider: "smtp", Operation: "send", Reason: "ok", Events: 12}},
		}
		if err := c.RecordWorkerProviderSignals(ctx, in); err != nil {
			t.Fatalf("RecordWorkerProviderSignals: %v", err)
		}
		got := fl.got.(coreapi.WorkerProviderSignals)
		if len(got.Counts) != 1 || got.Counts[0].Events != 12 {
			t.Errorf("counts = %+v, want the one 12-event delta", got.Counts)
		}
	})

	t.Run("AssignMailboxWorker", func(t *testing.T) {
		queue, err := c.AssignMailboxWorker(ctx, mailbox, ws)
		if err != nil {
			t.Fatalf("AssignMailboxWorker: %v", err)
		}
		if queue != "w:box-1" {
			t.Errorf("queue = %q, want w:box-1", queue)
		}
	})

	t.Run("RecordDeadLetter", func(t *testing.T) {
		in := coreapi.DeadLetterInput{
			WorkspaceID: ws, TaskType: "sequence:advance", Payload: []byte(`{"enrollment_id":"x"}`),
			LastError: "boom", AttemptCount: 25, Queue: "send",
		}
		if err := c.RecordDeadLetter(ctx, in); err != nil {
			t.Fatalf("RecordDeadLetter: %v", err)
		}
		got := fl.got.(coreapi.DeadLetterInput)
		// The payload is stored verbatim, so base64 round-tripping it is the
		// property that matters — a replay of anything else re-runs different work.
		if string(got.Payload) != `{"enrollment_id":"x"}` {
			t.Errorf("payload = %q, want it byte-for-byte", got.Payload)
		}
	})
}

// A worker that cannot reach the control plane gets an error and a ZERO value
// from every slice-4 method — never a guess, and never a value that would make
// the caller act.
//
// The direction matters per method and is asserted rather than assumed:
// sendNow=false does not schedule a send, matched=false does not swallow a
// campaign bounce, found=false falls back rather than inventing a label, and an
// empty queue is never returned alongside a nil error.
func TestEverySlice4MethodFailsClosedWhenTheControlPlaneDies(t *testing.T) {
	inbound, fl, jobs := &fakeInbound{found: true, matched: true}, &fakeFleet{queue: "w:box-1"}, &fakeJobs{sendNow: true}
	c, srv := serveSlice4(t, inbound, fl, jobs)
	ws, mailbox, send := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ctx := t.Context()

	// Prove the calls WORK first, so the failures below are attributable to the
	// control plane being gone rather than to a broken request.
	if _, _, err := c.NextWarmupDue(ctx, mailbox, ws); err != nil {
		t.Fatalf("NextWarmupDue before the control plane died: %v", err)
	}
	srv.Close()

	t.Run("SetInboxCursor", func(t *testing.T) {
		if err := c.SetInboxCursor(ctx, mailbox, ws, 1, 2); err == nil {
			t.Error("SetInboxCursor = nil error against a dead control plane")
		}
	})
	t.Run("SetInboxCursorString", func(t *testing.T) {
		if err := c.SetInboxCursorString(ctx, mailbox, ws, "x"); err == nil {
			t.Error("SetInboxCursorString = nil error against a dead control plane")
		}
	})
	t.Run("StoreInboundMessage", func(t *testing.T) {
		if err := c.StoreInboundMessage(ctx, coreapi.InboxMessageInput{WorkspaceID: ws, MailboxID: mailbox}); err == nil {
			t.Error("StoreInboundMessage = nil error against a dead control plane")
		}
	})
	t.Run("CaptureCRMReply", func(t *testing.T) {
		in := coreapi.CRMReplyInput{WorkspaceID: ws, EnrollmentID: uuid.NewString(), SendID: send}
		if err := c.CaptureCRMReply(ctx, in); err == nil {
			t.Error("CaptureCRMReply = nil error against a dead control plane")
		}
	})
	t.Run("IngestComplaint", func(t *testing.T) {
		err := c.IngestComplaint(ctx, coreapi.ComplaintInput{WorkspaceID: ws, SendID: send})
		if err == nil {
			t.Fatal("IngestComplaint = nil error against a dead control plane")
		}
		// And specifically NOT the permanent sentinel: an unreachable control
		// plane must be retried, not read as "this report is invalid forever".
		if errors.Is(err, coreapi.ErrInvalidComplaint) {
			t.Error("a dead control plane read as ErrInvalidComplaint; a transport failure must retry")
		}
	})
	t.Run("ResolveReplyLabel", func(t *testing.T) {
		label, ok, err := c.ResolveReplyLabel(ctx, ws, "positive")
		if err == nil {
			t.Fatal("ResolveReplyLabel = nil error against a dead control plane")
		}
		if ok || label != (coreapi.ReplyLabel{}) {
			t.Errorf("label = %+v ok = %v, want the zero label and false", label, ok)
		}
	})
	t.Run("RecordWarmupReceipt", func(t *testing.T) {
		plan, err := c.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
			WorkspaceID: ws, WarmupSendID: send, RecipientMailbox: mailbox,
		})
		if err == nil {
			t.Fatal("RecordWarmupReceipt = nil error against a dead control plane")
		}
		// A zero plan does nothing: no rescue, no mark-read, no reply.
		if plan != (coreapi.WarmupEngagePlan{}) {
			t.Errorf("plan = %+v, want the zero plan", plan)
		}
	})
	t.Run("FindWarmupSendByMessageID", func(t *testing.T) {
		ref, ok, err := c.FindWarmupSendByMessageID(ctx, ws, mailbox, "<x@y>")
		if err == nil {
			t.Fatal("FindWarmupSendByMessageID = nil error against a dead control plane")
		}
		if ok || ref != (coreapi.WarmupSendRef{}) {
			t.Errorf("ref = %+v ok = %v, want the zero ref and false", ref, ok)
		}
	})
	t.Run("RecordWarmupTokenFailure", func(t *testing.T) {
		if err := c.RecordWarmupTokenFailure(ctx, ws, mailbox, "ab", "bad"); err == nil {
			t.Error("RecordWarmupTokenFailure = nil error against a dead control plane")
		}
	})
	t.Run("RecordWarmupHardBounce", func(t *testing.T) {
		matched, err := c.RecordWarmupHardBounce(ctx, ws, "<x@y>", mailbox)
		if err == nil {
			t.Fatal("RecordWarmupHardBounce = nil error against a dead control plane")
		}
		// false, not true: a claimed match would swallow a real campaign bounce
		// and leave a dead address in the list.
		if matched {
			t.Error("matched = true on a failed call; a guessed match swallows a campaign bounce")
		}
	})
	t.Run("NextWarmupDue", func(t *testing.T) {
		due, sendNow, err := c.NextWarmupDue(ctx, mailbox, ws)
		if err == nil {
			t.Fatal("NextWarmupDue = nil error against a dead control plane")
		}
		if sendNow || !due.IsZero() {
			t.Errorf("due = %v sendNow = %v, want the zero time and false", due, sendNow)
		}
	})
	t.Run("UpsertWorkerHeartbeat", func(t *testing.T) {
		if err := c.UpsertWorkerHeartbeat(ctx, "box-1", "", "hostname"); err == nil {
			t.Error("UpsertWorkerHeartbeat = nil error against a dead control plane")
		}
	})
	t.Run("RecordWorkerProviderSignals", func(t *testing.T) {
		if err := c.RecordWorkerProviderSignals(ctx, coreapi.WorkerProviderSignals{WorkerID: "box-1"}); err == nil {
			t.Error("RecordWorkerProviderSignals = nil error against a dead control plane")
		}
	})
	t.Run("AssignMailboxWorker", func(t *testing.T) {
		queue, err := c.AssignMailboxWorker(ctx, mailbox, ws)
		if err == nil {
			t.Fatal("AssignMailboxWorker = nil error against a dead control plane")
		}
		if queue != "" {
			t.Errorf("queue = %q on a failed call, want empty", queue)
		}
	})
	t.Run("RecordDeadLetter", func(t *testing.T) {
		if err := c.RecordDeadLetter(ctx, coreapi.DeadLetterInput{TaskType: "x"}); err == nil {
			t.Error("RecordDeadLetter = nil error against a dead control plane")
		}
	})
}

// coreapi.ErrInvalidComplaint crosses as ITSELF, and an unrecognised 422 does
// not.
//
// This is the slice-4 analogue of TestAnUnrecognised404IsNotMistakenForAVanishedRow
// and it guards the larger failure of the two. The poller SKIPS an invalid
// complaint and RETRIES everything else, and it returns before SetInboxCursor
// either way — so reading a stray 422 as "permanently invalid" silently drops a
// real abuse report, while failing to read the real one wedges the mailbox's
// cursor and stops every inbound signal for it.
func TestAnInvalidComplaintCrossesAsItselfAndAStray422DoesNot(t *testing.T) {
	ws, send := uuid.NewString(), uuid.NewString()
	in := coreapi.ComplaintInput{WorkspaceID: ws, SendID: send, Email: "x@y.example", ProviderEventID: "arf:" + send}

	t.Run("the real sentinel", func(t *testing.T) {
		c, _ := serveSlice4(t, &fakeInbound{err: coreapi.ErrInvalidComplaint}, &fakeFleet{}, &fakeJobs{})
		err := c.IngestComplaint(t.Context(), in)
		if !errors.Is(err, coreapi.ErrInvalidComplaint) {
			t.Fatalf("IngestComplaint = %v, want coreapi.ErrInvalidComplaint", err)
		}
	})

	t.Run("a 422 with no known code", func(t *testing.T) {
		// An intermediary, or a control plane that grew a different 422. The
		// answer must be a plain error the poller RETRIES.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"error":"something else"}`)
		}))
		t.Cleanup(srv.Close)
		c, err := NewClient(srv.URL, testToken, true, &fakeOpener{})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		err = c.IngestComplaint(t.Context(), in)
		if err == nil {
			t.Fatal("IngestComplaint = nil error on a 422")
		}
		if errors.Is(err, coreapi.ErrInvalidComplaint) {
			t.Error("an unrecognised 422 read as ErrInvalidComplaint; a real abuse report would be dropped")
		}
	})
}

// The two lookups whose ORDINARY answer is "nothing" must answer 200 with
// found=false, never a 404.
//
// The distinction is not cosmetic. notFound() maps an unrecognised 404 body to
// a plain error, so a 404 here would turn the common case into a failed poll —
// and for ResolveReplyLabel specifically the caller is required to fall back to
// its pre-taxonomy class switch, which it cannot do if the call errored.
func TestTheOrdinaryEmptyAnswerIsNotA404(t *testing.T) {
	inbound := &fakeInbound{found: false, matched: false}
	c, _ := serveSlice4(t, inbound, &fakeFleet{}, &fakeJobs{})
	ws, mailbox := uuid.NewString(), uuid.NewString()

	label, ok, err := c.ResolveReplyLabel(t.Context(), ws, "some-deleted-key")
	if err != nil {
		t.Fatalf("ResolveReplyLabel with no label = %v, want nil error", err)
	}
	if ok || label != (coreapi.ReplyLabel{}) {
		t.Errorf("label = %+v ok = %v, want the zero label and false", label, ok)
	}

	ref, ok, err := c.FindWarmupSendByMessageID(t.Context(), ws, mailbox, "<not-warmup@x>")
	if err != nil {
		t.Fatalf("FindWarmupSendByMessageID with no match = %v, want nil error", err)
	}
	if ok || ref != (coreapi.WarmupSendRef{}) {
		t.Errorf("ref = %+v ok = %v, want the zero ref and false", ref, ok)
	}

	matched, err := c.RecordWarmupHardBounce(t.Context(), ws, "<campaign@x>", mailbox)
	if err != nil {
		t.Fatalf("RecordWarmupHardBounce with no match = %v, want nil error", err)
	}
	if matched {
		t.Error("matched = true with no match")
	}
}

// A slice-4 request that carries a workspace in its envelope AND inside its
// input is refused when the two disagree, before anything runs.
func TestSlice4InputRoutesRefuseAMismatchedWorkspace(t *testing.T) {
	inbound := &fakeInbound{}
	_, srv := serveSlice4(t, inbound, &fakeFleet{}, &fakeJobs{})
	envelope, inner, mailbox := uuid.NewString(), uuid.NewString(), uuid.NewString()

	for _, tc := range []struct {
		name string
		path string
		body any
	}{
		{"inbound message", PathInboxMessageStore, map[string]any{
			"workspace_id": envelope,
			"message":      coreapi.InboxMessageInput{WorkspaceID: inner, MailboxID: mailbox},
		}},
		{"crm reply", PathCRMReplyCapture, map[string]any{
			"workspace_id": envelope,
			"reply":        coreapi.CRMReplyInput{WorkspaceID: inner, SendID: uuid.NewString()},
		}},
		{"complaint", PathComplaintIngest, map[string]any{
			"workspace_id": envelope,
			"complaint":    coreapi.ComplaintInput{WorkspaceID: inner, SendID: uuid.NewString()},
		}},
		{"warmup receipt", PathWarmupReceipt, map[string]any{
			"workspace_id": envelope,
			"receipt":      coreapi.WarmupReceiptInput{WorkspaceID: inner, WarmupSendID: uuid.NewString()},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+tc.path, strings.NewReader(string(body)))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+testToken)
			req.Header.Set("Content-Type", "application/json")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 on a mismatched workspace", resp.StatusCode)
			}
		})
	}
	if inbound.calls != 0 {
		t.Errorf("the implementation was reached %d times on a refused request, want 0", inbound.calls)
	}
}

// The handler refuses to be built without either new writer, for the reason
// NewHandler refuses without the other four: since slice 4 a role=send worker
// has NO POOL, so a listener serving part of the transport is not a degraded
// worker, it is a worker that cannot poll a mailbox or heartbeat at all.
func TestTheHandlerRefusesWithoutTheSlice4Writers(t *testing.T) {
	full := Deps{
		Suppression: &fakeSuppression{}, Jobs: &fakeJobs{}, Outcomes: &fakeOutcomes{},
		InboxSends: &fakeInboxSends{}, Inbound: &fakeInbound{}, Fleet: &fakeFleet{},
	}
	if _, err := NewHandler(full, testToken, quietLogger()); err != nil {
		t.Fatalf("NewHandler with every writer: %v", err)
	}
	noInbound := full
	noInbound.Inbound = nil
	if _, err := NewHandler(noInbound, testToken, quietLogger()); err == nil {
		t.Error("NewHandler without an inbound writer = nil error")
	}
	noFleet := full
	noFleet.Fleet = nil
	if _, err := NewHandler(noFleet, testToken, quietLogger()); err == nil {
		t.Error("NewHandler without a fleet writer = nil error")
	}
}

// The control-plane-only methods refuse loudly rather than answering.
//
// A role=send worker never reaches them — internal/worker.Register gates
// registerScheduled on the role — so this is what happens if that stops being
// true. A nil-returning stub would report "no work due" and the sweep would
// quietly do nothing; the refusal names the method and the reason.
func TestTheCrossTenantSweepsRefuseRatherThanAnswering(t *testing.T) {
	c, _ := serveSlice4(t, &fakeInbound{}, &fakeFleet{}, &fakeJobs{})
	ctx := t.Context()

	if rows, err := c.ListDueEnrollments(ctx); !errors.Is(err, ErrControlPlaneOnly) || rows != nil {
		t.Errorf("ListDueEnrollments = (%v, %v), want (nil, ErrControlPlaneOnly)", rows, err)
	}
	if rows, err := c.ListActiveMailboxes(ctx); !errors.Is(err, ErrControlPlaneOnly) || rows != nil {
		t.Errorf("ListActiveMailboxes = (%v, %v), want (nil, ErrControlPlaneOnly)", rows, err)
	}
	if rows, err := c.ListDueWarmupMailboxes(ctx); !errors.Is(err, ErrControlPlaneOnly) || rows != nil {
		t.Errorf("ListDueWarmupMailboxes = (%v, %v), want (nil, ErrControlPlaneOnly)", rows, err)
	}
	if err := c.EvaluateWarmupHealth(ctx); !errors.Is(err, ErrControlPlaneOnly) {
		t.Errorf("EvaluateWarmupHealth = %v, want ErrControlPlaneOnly", err)
	}
	if rows, err := c.ListStaleSendingDomains(ctx, time.Hour); !errors.Is(err, ErrControlPlaneOnly) || rows != nil {
		t.Errorf("ListStaleSendingDomains = (%v, %v), want (nil, ErrControlPlaneOnly)", rows, err)
	}
	if err := c.RecordSendingDomainAuth(ctx, coreapi.SendingDomainAuth{}); !errors.Is(err, ErrControlPlaneOnly) {
		t.Errorf("RecordSendingDomainAuth = %v, want ErrControlPlaneOnly", err)
	}

	// And the refusal NAMES the method, so a log line is actionable even though
	// the sentinel is shared.
	err := c.EvaluateWarmupHealth(ctx)
	if !strings.Contains(err.Error(), "EvaluateWarmupHealth") {
		t.Errorf("%v does not name the method", err)
	}
}

// No slice-4 route is served on GET, PUT or DELETE. The mux registers "POST
// <path>" only; this pins that nothing added in this slice widened it.
func TestSlice4RoutesArePOSTOnly(t *testing.T) {
	_, srv := serveSlice4(t, &fakeInbound{}, &fakeFleet{}, &fakeJobs{})
	for _, path := range slice4Paths() {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, http.NoBody)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+testToken)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Errorf("%s %s = 200, want a refusal", method, path)
			}
		}
	}
}

// Every slice-4 route requires the fleet token, like every route before it.
func TestSlice4RoutesRequireTheFleetToken(t *testing.T) {
	inbound, fl := &fakeInbound{}, &fakeFleet{}
	_, srv := serveSlice4(t, inbound, fl, &fakeJobs{})
	for _, path := range slice4Paths() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("unauthenticated POST %s = %d, want 401", path, resp.StatusCode)
		}
	}
	if inbound.calls != 0 || fl.calls != 0 {
		t.Errorf("an unauthenticated request reached the implementation (%d inbound, %d fleet)", inbound.calls, fl.calls)
	}
}

func slice4Paths() []string {
	return []string{
		PathInboxCursorUID, PathInboxCursorString, PathInboxPollFailure, PathInboxMessageStore,
		PathCRMReplyCapture, PathComplaintIngest, PathReplyLabelResolve,
		PathWarmupReceipt, PathWarmupSendByMessageID, PathWarmupTokenFailure,
		PathWarmupHardBounce, PathWarmupNextDue,
		PathWorkerHeartbeat, PathWorkerProviderSignals, PathMailboxWorkerAssign,
		PathDeadLetterRecord,
	}
}
