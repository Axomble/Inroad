package warmup

import (
	"context"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
)

// stubCore is a zero-value no-op implementation of the whole coreapi.Client
// surface. Tests embed it and override only the handful of warmup methods the
// case under test exercises, so the fakes stay small and a new coreapi method
// breaks compilation here (one place) rather than in every test.
type stubCore struct{}

var _ coreapi.Client = stubCore{}

func (stubCore) MailboxExists(context.Context, string) (bool, error) { return false, nil }
func (stubCore) GetSendJob(context.Context, string, string) (coreapi.SendJob, error) {
	return coreapi.SendJob{}, nil
}
func (stubCore) ClaimSend(context.Context, string, string) (bool, error) { return false, nil }
func (stubCore) ReleaseSend(context.Context, string, string) error       { return nil }
func (stubCore) MarkSend(context.Context, string, string, coreapi.SendResult) error {
	return nil
}
func (stubCore) ListStuckQueuedSends(context.Context) ([]coreapi.StuckSend, error) {
	return nil, nil
}
func (stubCore) IncrementSendAttempts(context.Context, string, string) (int, error) {
	return 0, nil
}
func (stubCore) GetStepSendJob(context.Context, string, string) (coreapi.StepSendJob, error) {
	return coreapi.StepSendJob{}, nil
}
func (stubCore) ClaimStepSend(context.Context, coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	return coreapi.ClaimSkip, nil
}
func (stubCore) MarkStepDelivered(context.Context, coreapi.StepSendJob, string) error { return nil }
func (stubCore) AdvanceStepCursor(context.Context, coreapi.StepSendJob) (coreapi.Advance, error) {
	return coreapi.Advance{}, nil
}
func (stubCore) FinalizeStepSend(context.Context, coreapi.StepSendJob, coreapi.StepResult) (coreapi.Advance, error) {
	return coreapi.Advance{}, nil
}
func (stubCore) ReleaseStepSend(context.Context, coreapi.StepSendJob) error    { return nil }
func (stubCore) MarkStepStopped(context.Context, string, string, string) error { return nil }
func (stubCore) DeferEnrollment(context.Context, string, string, time.Time) error {
	return nil
}
func (stubCore) IncrementEnrollmentCapDeferrals(context.Context, string, string) (int, error) {
	return 0, nil
}
func (stubCore) ListDueEnrollments(context.Context) ([]coreapi.DueEnrollment, error) {
	return nil, nil
}
func (stubCore) ListActiveMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return nil, nil
}
func (stubCore) GetInboxPollJob(context.Context, string, string) (coreapi.InboxPollJob, error) {
	return coreapi.InboxPollJob{}, nil
}
func (stubCore) SetInboxCursor(context.Context, string, string, uint32, uint32) error { return nil }
func (stubCore) SetInboxCursorString(context.Context, string, string, string) error   { return nil }
func (stubCore) FindSendByMessageID(context.Context, string, string) (coreapi.SendRef, error) {
	return coreapi.SendRef{}, nil
}
func (stubCore) MarkReplied(context.Context, string, string, string, string, float64) error {
	return nil
}
func (stubCore) MarkUnsubscribed(context.Context, string, string, string) error { return nil }
func (stubCore) RecordReplyClass(context.Context, string, string, string, string, float64) error {
	return nil
}
func (stubCore) MarkBounced(context.Context, string, string, string, bool) error { return nil }
func (stubCore) UpsertWorkerHeartbeat(context.Context, string, string) error     { return nil }
func (stubCore) AssignMailboxWorker(context.Context, string, string) (string, error) {
	return "", nil
}
func (stubCore) GetWarmupSendJob(context.Context, string, string) (coreapi.WarmupSendJob, error) {
	return coreapi.WarmupSendJob{}, nil
}
func (stubCore) ClaimWarmupSend(context.Context, coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	return coreapi.ClaimWon, nil
}
func (stubCore) MarkWarmupSent(context.Context, coreapi.WarmupSendJob, string) error { return nil }
func (stubCore) ReleaseWarmupSend(context.Context, coreapi.WarmupSendJob) error      { return nil }
func (stubCore) FailWarmupSend(context.Context, coreapi.WarmupSendJob, string) error { return nil }
func (stubCore) NextWarmupDue(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (stubCore) RecordWarmupReceipt(context.Context, coreapi.WarmupReceiptInput) (coreapi.WarmupEngagePlan, error) {
	return coreapi.WarmupEngagePlan{}, nil
}
func (stubCore) GetWarmupEngageJob(context.Context, string, string) (coreapi.WarmupEngageJob, error) {
	return coreapi.WarmupEngageJob{}, nil
}
func (stubCore) MarkWarmupEngaged(context.Context, string, string, bool) error { return nil }
func (stubCore) ListDueWarmupMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return nil, nil
}
func (stubCore) EvaluateWarmupHealth(context.Context) error { return nil }
func (stubCore) ListStaleSendingDomains(context.Context, time.Duration) ([]coreapi.SendingDomainRef, error) {
	return nil, nil
}
func (stubCore) RecordSendingDomainAuth(context.Context, coreapi.SendingDomainAuth) error {
	return nil
}

// --- shared test doubles for Sender and Enqueuer ---

type tickCall struct {
	mailboxID   string
	workspaceID string
	dest        string
	at          time.Time
}

type fakeEnq struct {
	calls []tickCall
	err   error
	// failOn scopes err to a single mailbox: when set, only that mailbox's
	// enqueue returns err and the rest succeed (proves per-mailbox isolation).
	// When empty, err applies to every call (default nil = all succeed).
	failOn string
}

func (f *fakeEnq) EnqueueWarmupTickAt(mailboxID, workspaceID string, t time.Time, dest string) error {
	f.calls = append(f.calls, tickCall{mailboxID, workspaceID, dest, t})
	if f.failOn != "" && mailboxID != f.failOn {
		return nil
	}
	return f.err
}

type fakeSender struct {
	calls  int
	gotJob mail.OutboundJob
	gotMsg mail.Message
	msgID  string
	err    error
}

func (f *fakeSender) Send(_ context.Context, tj mail.OutboundJob, msg mail.Message) (string, error) {
	f.calls++
	f.gotJob = tj
	f.gotMsg = msg
	return f.msgID, f.err
}
