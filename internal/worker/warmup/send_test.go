package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"syscall"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

// sendCore drives the send handler: a programmable job/claim/send outcome plus
// recording of which finalizers ran, so each path can be asserted without a DB.
type sendCore struct {
	stubCore

	job      coreapi.WarmupSendJob
	claim    coreapi.ClaimOutcome
	dueAt    time.Time
	dueNow   bool
	assigned string

	marked      bool
	markedMsgID string
	released    bool
	failed      bool
	failedMsg   string
	assignCalls int
	nextCalls   int
}

func (c *sendCore) GetWarmupSendJob(context.Context, string, string) (coreapi.WarmupSendJob, error) {
	return c.job, nil
}
func (c *sendCore) ClaimWarmupSend(context.Context, coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	return c.claim, nil
}
func (c *sendCore) MarkWarmupSent(_ context.Context, _ coreapi.WarmupSendJob, msgID string) error {
	c.marked = true
	c.markedMsgID = msgID
	return nil
}
func (c *sendCore) ReleaseWarmupSend(context.Context, coreapi.WarmupSendJob) error {
	c.released = true
	return nil
}
func (c *sendCore) FailWarmupSend(_ context.Context, _ coreapi.WarmupSendJob, msg string) error {
	c.failed = true
	c.failedMsg = msg
	return nil
}
func (c *sendCore) NextWarmupDue(context.Context, string, string) (time.Time, bool, error) {
	c.nextCalls++
	return c.dueAt, c.dueNow, nil
}
func (c *sendCore) AssignMailboxWorker(context.Context, string, string) (string, error) {
	c.assignCalls++
	return c.assigned, nil
}

func tickTask(t *testing.T, mailboxID, workspaceID string) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.WarmupTickPayload{MailboxID: mailboxID, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(queue.TaskWarmupTick, b)
}

func wonJob() coreapi.WarmupSendJob {
	return coreapi.WarmupSendJob{
		WorkspaceID: "ws-1", FromMailbox: "mb-1", ToMailbox: "mb-2",
		SendID: "send-1", ToEmail: "to@x.com", FromEmail: "from@x.com", FromName: "From",
		Subject: "hi", BodyText: "body", Token: "tok-abc", Provider: "smtp",
		SMTPHost: "smtp.x.com", SMTPPort: 587, SMTPUsername: "u",
		SMTPPassword: []byte("secret"),
	}
}

func TestSendHappyPathSendsSetsHeaderMarksAndSchedules(t *testing.T) {
	due := time.Now().Add(37 * time.Minute).Truncate(time.Second)
	core := &sendCore{job: wonJob(), claim: coreapi.ClaimWon, dueAt: due, assigned: "w:node-a"}
	snd := &fakeSender{msgID: "<mid-1@x.com>"}
	enq := &fakeEnq{}

	if err := SendHandler(core, snd, enq)(context.Background(), tickTask(t, "mb-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if snd.calls != 1 {
		t.Fatalf("Send calls = %d, want 1", snd.calls)
	}
	// The signed receipt header MUST be on the outgoing message (the poller reads
	// it off the wire in C5).
	if got := snd.gotMsg.ExtraHeaders[warmupHeader]; got != "tok-abc" {
		t.Fatalf("%s header = %q, want tok-abc", warmupHeader, got)
	}
	if snd.gotMsg.Subject != "hi" || snd.gotMsg.To != "to@x.com" {
		t.Fatalf("message envelope not built from job: %+v", snd.gotMsg)
	}
	if snd.gotJob.Password != "secret" || snd.gotJob.Provider != "smtp" {
		t.Fatalf("transport not built from job: %+v", snd.gotJob)
	}
	if !core.marked || core.markedMsgID != "<mid-1@x.com>" {
		t.Fatalf("MarkWarmupSent not called with msg id: marked=%v id=%q", core.marked, core.markedMsgID)
	}
	if core.released || core.failed {
		t.Fatalf("unexpected release/fail on happy path")
	}
	// Lazy chain: one tick scheduled at the next-due time, routed to the assigned
	// worker queue (per-IP routing).
	if len(enq.calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1", len(enq.calls))
	}
	got := enq.calls[0]
	if got.mailboxID != "mb-1" || got.workspaceID != "ws-1" || got.dest != "w:node-a" || !got.at.Equal(due) {
		t.Fatalf("scheduled tick = %+v, want mb-1/ws-1/w:node-a/%v", got, due)
	}
	if core.assignCalls != 1 {
		t.Fatalf("AssignMailboxWorker calls = %d, want 1", core.assignCalls)
	}
}

func TestSendZeroizesSecretsAfterSend(t *testing.T) {
	job := wonJob()
	job.AccessToken = []byte("bearer")
	core := &sendCore{job: job, claim: coreapi.ClaimWon, assigned: ""}
	snd := &fakeSender{msgID: "<m@x>"}

	if err := SendHandler(core, snd, &fakeEnq{})(context.Background(), tickTask(t, "mb-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	// The handler holds the SAME backing slices the job exposed; after the deferred
	// zeroize they must be wiped.
	for i, b := range job.SMTPPassword {
		if b != 0 {
			t.Fatalf("SMTPPassword[%d] = %d, not zeroized", i, b)
		}
	}
	for i, b := range job.AccessToken {
		if b != 0 {
			t.Fatalf("AccessToken[%d] = %d, not zeroized", i, b)
		}
	}
}

func TestSendRetryableReleasesAndReturnsError(t *testing.T) {
	core := &sendCore{job: wonJob(), claim: coreapi.ClaimWon}
	// syscall.ECONNREFUSED is classified Retryable (nothing delivered).
	snd := &fakeSender{err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED)}
	enq := &fakeEnq{}

	err := SendHandler(core, snd, enq)(context.Background(), tickTask(t, "mb-1", "ws-1"))
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("handler err = %v, want the transient send error (asynq retry)", err)
	}
	if !core.released {
		t.Fatalf("ReleaseWarmupSend not called on transient failure")
	}
	if core.marked || core.failed {
		t.Fatalf("unexpected mark/fail on transient failure")
	}
	if len(enq.calls) != 0 {
		t.Fatalf("no next tick should be scheduled on a released transient failure, got %d", len(enq.calls))
	}
}

func TestSendPermanentFailsAndSchedulesNext(t *testing.T) {
	due := time.Now().Add(time.Hour).Truncate(time.Second)
	core := &sendCore{job: wonJob(), claim: coreapi.ClaimWon, dueAt: due, assigned: "w:node-b"}
	// A plain (unknown) error is NOT retryable -> permanent (fail-forward).
	snd := &fakeSender{err: errors.New("550 mailbox unavailable")}
	enq := &fakeEnq{}

	if err := SendHandler(core, snd, enq)(context.Background(), tickTask(t, "mb-1", "ws-1")); err != nil {
		t.Fatalf("permanent failure should be swallowed (fail-forward), got %v", err)
	}
	if !core.failed || core.failedMsg == "" {
		t.Fatalf("FailWarmupSend not called with a message: failed=%v msg=%q", core.failed, core.failedMsg)
	}
	if core.marked || core.released {
		t.Fatalf("unexpected mark/release on permanent failure")
	}
	// Chain stays alive so one bad send doesn't wedge the mailbox.
	if len(enq.calls) != 1 || enq.calls[0].dest != "w:node-b" {
		t.Fatalf("expected one next tick to w:node-b, got %+v", enq.calls)
	}
}

func TestSendSkipsWhenJobSkip(t *testing.T) {
	core := &sendCore{job: coreapi.WarmupSendJob{Skip: true}, claim: coreapi.ClaimWon}
	snd := &fakeSender{}
	enq := &fakeEnq{}

	if err := SendHandler(core, snd, enq)(context.Background(), tickTask(t, "mb-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if snd.calls != 0 {
		t.Fatalf("Send called on a Skip job")
	}
	if len(enq.calls) != 0 || core.nextCalls != 0 {
		t.Fatalf("a Skip job must not schedule or send; enq=%d next=%d", len(enq.calls), core.nextCalls)
	}
}

func TestSendClaimSkipDoesNothing(t *testing.T) {
	core := &sendCore{job: wonJob(), claim: coreapi.ClaimSkip}
	snd := &fakeSender{}
	enq := &fakeEnq{}

	if err := SendHandler(core, snd, enq)(context.Background(), tickTask(t, "mb-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if snd.calls != 0 || len(enq.calls) != 0 {
		t.Fatalf("ClaimSkip must not send or schedule; send=%d enq=%d", snd.calls, len(enq.calls))
	}
}

func TestSendClaimAlreadySentSchedulesWithoutSending(t *testing.T) {
	due := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	core := &sendCore{job: wonJob(), claim: coreapi.ClaimAlreadySent, dueAt: due, assigned: "w:node-c"}
	snd := &fakeSender{}
	enq := &fakeEnq{}

	if err := SendHandler(core, snd, enq)(context.Background(), tickTask(t, "mb-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if snd.calls != 0 {
		t.Fatalf("ClaimAlreadySent must NOT re-send, got %d sends", snd.calls)
	}
	if core.marked {
		t.Fatalf("ClaimAlreadySent must not re-mark")
	}
	if len(enq.calls) != 1 || enq.calls[0].dest != "w:node-c" || !enq.calls[0].at.Equal(due) {
		t.Fatalf("recover-forward should schedule the next tick, got %+v", enq.calls)
	}
}

func TestSendUsesNowWhenNextDueIsSendNow(t *testing.T) {
	core := &sendCore{job: wonJob(), claim: coreapi.ClaimWon, dueNow: true, assigned: "w:node-a"}
	snd := &fakeSender{msgID: "<m@x>"}
	enq := &fakeEnq{}

	before := time.Now()
	if err := SendHandler(core, snd, enq)(context.Background(), tickTask(t, "mb-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(enq.calls) != 1 {
		t.Fatalf("enqueue calls = %d, want 1", len(enq.calls))
	}
	// sendNow overrides the (zero) due time with ~now.
	if enq.calls[0].at.Before(before) {
		t.Fatalf("sendNow tick scheduled at %v, want >= %v", enq.calls[0].at, before)
	}
}
