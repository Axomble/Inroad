package warmup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"syscall"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// engageCore drives the engage handler: a programmable engage job + reply claim
// outcome, recording which finalizers ran and the MarkWarmupEngaged(replied) it was
// called with, so each path is asserted without a DB.
type engageCore struct {
	stubCore

	job   coreapi.WarmupEngageJob
	claim coreapi.ClaimOutcome

	marked   bool
	released bool
	failed   bool

	engagedCalled  bool
	engagedReplied bool
}

func (c *engageCore) GetWarmupEngageJob(context.Context, string, string) (coreapi.WarmupEngageJob, error) {
	return c.job, nil
}
func (c *engageCore) ClaimWarmupSend(context.Context, coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	return c.claim, nil
}
func (c *engageCore) MarkWarmupSent(context.Context, coreapi.WarmupSendJob, string) error {
	c.marked = true
	return nil
}
func (c *engageCore) ReleaseWarmupSend(context.Context, coreapi.WarmupSendJob) error {
	c.released = true
	return nil
}
func (c *engageCore) FailWarmupSend(context.Context, coreapi.WarmupSendJob, string) error {
	c.failed = true
	return nil
}
func (c *engageCore) MarkWarmupEngaged(_ context.Context, _, _ string, replied bool) error {
	c.engagedCalled = true
	c.engagedReplied = replied
	return nil
}

// fakeEngager records the recipient-side actions and can inject a per-action error
// (e.g. mail.ErrEngageUnsupported for the Graph skip path).
type fakeEngager struct {
	rescueCalls   int
	markReadCalls int
	lastTarget    mail.EngageTarget
	// imapPwAtCall snapshots the IMAP password string AT CALL TIME (before the
	// handler's deferred zeroize wipes the shared backing slice), so a test can prove
	// the live secret was passed without racing the zeroize.
	imapPwAtCall string
	rescueErr    error
	markReadErr  error
}

func (f *fakeEngager) Rescue(_ context.Context, t mail.EngageTarget) error {
	f.rescueCalls++
	f.lastTarget = t
	f.imapPwAtCall = string(t.IMAPPassword)
	return f.rescueErr
}
func (f *fakeEngager) MarkRead(_ context.Context, t mail.EngageTarget) error {
	f.markReadCalls++
	f.lastTarget = t
	f.imapPwAtCall = string(t.IMAPPassword)
	return f.markReadErr
}

func engageTask(t *testing.T, receiptID, workspaceID string) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.WarmupEngagePayload{ReceiptID: receiptID, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(queue.TaskWarmupEngage, b)
}

// replyJob is a fully-formed reply warmup send FROM the recipient.
func replyJob() coreapi.WarmupSendJob {
	return coreapi.WarmupSendJob{
		WorkspaceID: "ws-1", FromMailbox: "mb-2", ToMailbox: "mb-1", ThreadID: "th-1",
		IsReply: true, SendID: "reply-1", ToEmail: "orig@x.com", FromEmail: "recip@x.com",
		FromName: "Recip", Subject: "Re: hi", BodyText: "thanks", Token: "reply-tok",
		Provider: "smtp", SMTPHost: "smtp.x.com", SMTPPort: 587, SMTPUsername: "u",
		SMTPPassword: []byte("secret"),
	}
}

// engageJob is a spam-placed smtp receipt that rescues, reads, and replies.
func engageJob() coreapi.WarmupEngageJob {
	return coreapi.WarmupEngageJob{
		Provider: "smtp", IMAPHost: "imap.x.com", IMAPPort: 993, IMAPUsername: "u",
		SMTPHost: "smtp.x.com", SMTPPort: 587, SMTPUsername: "u", SMTPPassword: []byte("secret"),
		SourceFolder: "Junk", MessageID: "<abc@x.com>",
		DoRescue: true, DoMarkRead: true, DoReply: true, ReplySend: replyJob(),
	}
}

func TestEngageRescueReadReplyHappyPath(t *testing.T) {
	core := &engageCore{job: engageJob(), claim: coreapi.ClaimWon}
	eng := &fakeEngager{}
	snd := &fakeSender{msgID: "<reply-mid@x.com>"}

	if err := EngageHandler(core, eng, snd)(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if eng.rescueCalls != 1 || eng.markReadCalls != 1 {
		t.Fatalf("engage calls: rescue=%d markread=%d, want 1/1", eng.rescueCalls, eng.markReadCalls)
	}
	// The engage target is built from the job: IMAP transport + the receipt locator.
	if eng.lastTarget.SourceFolder != "Junk" || eng.lastTarget.MessageID != "<abc@x.com>" {
		t.Fatalf("engage target locator = %+v, want Junk/<abc@x.com>", eng.lastTarget)
	}
	if eng.lastTarget.IMAPHost != "imap.x.com" || eng.imapPwAtCall != "secret" {
		t.Fatalf("engage target imap transport not built from job: host=%q pw@call=%q", eng.lastTarget.IMAPHost, eng.imapPwAtCall)
	}
	// The reply was claimed, sent (with its token header + envelope), and finalized.
	if snd.calls != 1 {
		t.Fatalf("reply Send calls = %d, want 1", snd.calls)
	}
	if got := snd.gotMsg.ExtraHeaders[warmupHeader]; got != "reply-tok" {
		t.Fatalf("reply %s header = %q, want reply-tok", warmupHeader, got)
	}
	if snd.gotMsg.To != "orig@x.com" || snd.gotMsg.Subject != "Re: hi" {
		t.Fatalf("reply envelope not built from ReplySend: %+v", snd.gotMsg)
	}
	if !core.marked {
		t.Fatalf("MarkWarmupSent not called for the reply")
	}
	if !core.engagedCalled || !core.engagedReplied {
		t.Fatalf("MarkWarmupEngaged: called=%v replied=%v, want true/true", core.engagedCalled, core.engagedReplied)
	}
}

func TestEngageSkipsRescueWhenNotSpam(t *testing.T) {
	job := engageJob()
	job.DoRescue = false
	core := &engageCore{job: job, claim: coreapi.ClaimWon}
	eng := &fakeEngager{}

	if err := EngageHandler(core, eng, &fakeSender{msgID: "<m@x>"})(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if eng.rescueCalls != 0 {
		t.Fatalf("Rescue called on a non-spam placement (%d)", eng.rescueCalls)
	}
	if eng.markReadCalls != 1 {
		t.Fatalf("MarkRead calls = %d, want 1", eng.markReadCalls)
	}
}

func TestEngageSkipsReplyWhenDoReplyFalse(t *testing.T) {
	job := engageJob()
	job.DoReply = false
	core := &engageCore{job: job, claim: coreapi.ClaimWon}
	eng := &fakeEngager{}
	snd := &fakeSender{}

	if err := EngageHandler(core, eng, snd)(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if snd.calls != 0 {
		t.Fatalf("reply sent when DoReply=false (%d)", snd.calls)
	}
	if !core.engagedCalled || core.engagedReplied {
		t.Fatalf("MarkWarmupEngaged: called=%v replied=%v, want called with replied=false", core.engagedCalled, core.engagedReplied)
	}
}

func TestEngageUnsupportedProviderSkipsGracefully(t *testing.T) {
	// Graph/M365: both engage steps return ErrEngageUnsupported; the handler logs a
	// skip and still completes (no error, receipt marked engaged). The skip must be
	// OBSERVABLE, not silently swallowed, so we capture slog and assert a log line.
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	job := engageJob()
	job.Provider = "m365"
	job.DoReply = false
	core := &engageCore{job: job, claim: coreapi.ClaimWon}
	eng := &fakeEngager{rescueErr: mail.ErrEngageUnsupported, markReadErr: mail.ErrEngageUnsupported}

	if err := EngageHandler(core, eng, &fakeSender{})(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
		t.Fatalf("unsupported provider must be a clean skip, got %v", err)
	}
	if eng.rescueCalls != 1 || eng.markReadCalls != 1 {
		t.Fatalf("both steps should be attempted before skip: rescue=%d markread=%d", eng.rescueCalls, eng.markReadCalls)
	}
	if !core.engagedCalled || core.engagedReplied {
		t.Fatalf("MarkWarmupEngaged: called=%v replied=%v, want called with replied=false", core.engagedCalled, core.engagedReplied)
	}
	// The skip is observable: a log line per skipped step names the provider (not a
	// silent swallow).
	if out := logs.String(); !strings.Contains(out, "step unsupported for provider") || !strings.Contains(out, "provider=m365") {
		t.Fatalf("unsupported skip not logged observably; slog output:\n%s", out)
	}
}

// errBoom is a non-unsupported engager failure the handler must surface verbatim
// (wrapped with %w) so a caller can match it via errors.Is.
var errBoom = errors.New("imap timeout")

func TestEngageEngagerErrorPropagates(t *testing.T) {
	// A non-unsupported engager error is surfaced so asynq retries, and the receipt
	// is NOT marked engaged (so the retry re-engages).
	core := &engageCore{job: engageJob(), claim: coreapi.ClaimWon}
	eng := &fakeEngager{rescueErr: errBoom}

	err := EngageHandler(core, eng, &fakeSender{})(context.Background(), engageTask(t, "rcpt-1", "ws-1"))
	if !errors.Is(err, errBoom) {
		t.Fatalf("engager error = %v, want the specific errBoom wrapped for retry", err)
	}
	if core.engagedCalled {
		t.Fatalf("receipt marked engaged despite an engage failure (retry would skip it)")
	}
}

// TestEngageMarkReadFolderFollowsPlacement proves the handler tells the engager WHICH
// folder to mark-read in: a rescued spam message is read in INBOX (empty sentinel), while
// a non-inbox, non-spam ("other") placement is read in its OWN SourceFolder.
func TestEngageMarkReadFolderFollowsPlacement(t *testing.T) {
	t.Run("other placement reads in its own folder", func(t *testing.T) {
		job := engageJob()
		job.DoRescue = false // 'other' placement: no rescue
		job.SourceFolder = "Archive"
		job.DoReply = false
		core := &engageCore{job: job, claim: coreapi.ClaimWon}
		eng := &fakeEngager{}

		if err := EngageHandler(core, eng, &fakeSender{})(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
			t.Fatalf("handler: %v", err)
		}
		if eng.markReadCalls != 1 {
			t.Fatalf("MarkRead calls = %d, want 1", eng.markReadCalls)
		}
		if eng.lastTarget.MarkReadFolder != "Archive" {
			t.Fatalf("mark-read folder = %q, want the message's own folder \"Archive\"", eng.lastTarget.MarkReadFolder)
		}
	})
	t.Run("rescued spam placement reads in inbox", func(t *testing.T) {
		job := engageJob() // DoRescue=true, SourceFolder="Junk"
		job.DoReply = false
		core := &engageCore{job: job, claim: coreapi.ClaimWon}
		eng := &fakeEngager{}

		if err := EngageHandler(core, eng, &fakeSender{})(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
			t.Fatalf("handler: %v", err)
		}
		// Empty MarkReadFolder ⇒ INBOX in the engager: the message moved there on rescue.
		if eng.lastTarget.MarkReadFolder != "" {
			t.Fatalf("rescued mark-read folder = %q, want empty (INBOX) after rescue", eng.lastTarget.MarkReadFolder)
		}
	})
}

func TestEngageReplyAlreadySentRecoversForward(t *testing.T) {
	// A prior run delivered the reply but didn't finalize the engage: the claim is
	// ClaimAlreadySent, so the reply is NOT re-sent but still counts as replied.
	core := &engageCore{job: engageJob(), claim: coreapi.ClaimAlreadySent}
	snd := &fakeSender{}

	if err := EngageHandler(core, &fakeEngager{}, snd)(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if snd.calls != 0 {
		t.Fatalf("ClaimAlreadySent must NOT re-send the reply, got %d", snd.calls)
	}
	if core.marked {
		t.Fatalf("ClaimAlreadySent must not re-mark the reply sent")
	}
	if !core.engagedReplied {
		t.Fatalf("recover-forward reply should still count as replied")
	}
}

func TestEngageReplyClaimSkipDoesNotReply(t *testing.T) {
	core := &engageCore{job: engageJob(), claim: coreapi.ClaimSkip}
	snd := &fakeSender{}

	if err := EngageHandler(core, &fakeEngager{}, snd)(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if snd.calls != 0 {
		t.Fatalf("ClaimSkip must not send the reply, got %d", snd.calls)
	}
	if core.engagedReplied {
		t.Fatalf("a skipped reply claim must mark engaged with replied=false")
	}
	if !core.engagedCalled {
		t.Fatalf("MarkWarmupEngaged must still run to close out the engagement")
	}
}

func TestEngageReplyRetryableReleasesAndReturnsError(t *testing.T) {
	core := &engageCore{job: engageJob(), claim: coreapi.ClaimWon}
	snd := &fakeSender{err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED)}

	err := EngageHandler(core, &fakeEngager{}, snd)(context.Background(), engageTask(t, "rcpt-1", "ws-1"))
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("handler err = %v, want the transient reply error", err)
	}
	if !core.released {
		t.Fatalf("ReleaseWarmupSend not called on a transient reply failure")
	}
	if core.engagedCalled {
		t.Fatalf("must NOT mark engaged on a retryable reply failure (retry re-engages)")
	}
}

func TestEngageReplyPermanentFailsForward(t *testing.T) {
	core := &engageCore{job: engageJob(), claim: coreapi.ClaimWon}
	snd := &fakeSender{err: errors.New("550 mailbox unavailable")}

	if err := EngageHandler(core, &fakeEngager{}, snd)(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
		t.Fatalf("permanent reply failure should be fail-forward (nil), got %v", err)
	}
	if !core.failed {
		t.Fatalf("FailWarmupSend not called on a permanent reply failure")
	}
	if !core.engagedCalled || core.engagedReplied {
		t.Fatalf("permanent reply failure: engaged=%v replied=%v, want engaged with replied=false", core.engagedCalled, core.engagedReplied)
	}
}

func TestEngageZeroizesSecretsAfterUse(t *testing.T) {
	job := engageJob()
	job.AccessToken = []byte("bearer")
	core := &engageCore{job: job, claim: coreapi.ClaimWon}

	if err := EngageHandler(core, &fakeEngager{}, &fakeSender{msgID: "<m@x>"})(context.Background(), engageTask(t, "rcpt-1", "ws-1")); err != nil {
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
