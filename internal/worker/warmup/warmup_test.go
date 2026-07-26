package warmup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

type fakeCore struct{ exists bool }

func (f fakeCore) MailboxExists(context.Context, string) (bool, error) { return f.exists, nil }

func (f fakeCore) GetSendJob(context.Context, string, string) (coreapi.SendJob, error) {
	return coreapi.SendJob{}, nil
}

func (f fakeCore) MarkSend(context.Context, string, string, coreapi.SendResult) error { return nil }

func (f fakeCore) ListStuckQueuedSends(context.Context) ([]coreapi.StuckSend, error) {
	return nil, nil
}

func (f fakeCore) IncrementSendAttempts(context.Context, string, string) (int, error) {
	return 0, nil
}

func (f fakeCore) GetStepSendJob(context.Context, string, string) (coreapi.StepSendJob, error) {
	return coreapi.StepSendJob{}, nil
}

func (f fakeCore) ClaimSend(context.Context, string, string) (bool, error) { return true, nil }
func (f fakeCore) ReleaseSend(context.Context, string, string) error       { return nil }

func (f fakeCore) ClaimStepSend(context.Context, coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	return coreapi.ClaimWon, nil
}
func (f fakeCore) MarkStepDelivered(context.Context, coreapi.StepSendJob, string) error { return nil }
func (f fakeCore) AdvanceStepCursor(context.Context, coreapi.StepSendJob) (coreapi.Advance, error) {
	return coreapi.Advance{}, nil
}
func (f fakeCore) FinalizeStepSend(context.Context, coreapi.StepSendJob, coreapi.StepResult) (coreapi.Advance, error) {
	return coreapi.Advance{}, nil
}
func (f fakeCore) ReleaseStepSend(context.Context, coreapi.StepSendJob) error { return nil }

func (f fakeCore) MarkStepStopped(context.Context, string, string, string) error { return nil }

func (f fakeCore) IncrementEnrollmentCapDeferrals(context.Context, string, string) (int, error) {
	return 0, nil
}

func (f fakeCore) ListDueEnrollments(context.Context) ([]coreapi.DueEnrollment, error) {
	return nil, nil
}

func (f fakeCore) ListActiveMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return nil, nil
}

func (f fakeCore) GetInboxPollJob(context.Context, string, string) (coreapi.InboxPollJob, error) {
	return coreapi.InboxPollJob{}, nil
}

func (f fakeCore) SetInboxCursor(context.Context, string, string, uint32, uint32) error { return nil }

func (f fakeCore) SetInboxCursorString(context.Context, string, string, string) error { return nil }

func (f fakeCore) FindSendByMessageID(context.Context, string, string) (coreapi.SendRef, error) {
	return coreapi.SendRef{}, nil
}

func (f fakeCore) MarkReplied(context.Context, string, string, string, string, float64) error {
	return nil
}

func (f fakeCore) MarkUnsubscribed(context.Context, string, string, string) error { return nil }

func (f fakeCore) RecordReplyClass(context.Context, string, string, string, string, float64) error {
	return nil
}

func (f fakeCore) MarkBounced(context.Context, string, string, string, bool) error { return nil }

func (f fakeCore) UpsertWorkerHeartbeat(context.Context, string, string) error { return nil }

func (f fakeCore) AssignMailboxWorker(context.Context, string, string) (string, error) {
	return "", nil
}

var _ coreapi.Client = fakeCore{}

func TestWarmupHandlerSkipsUnknownMailbox(t *testing.T) {
	h := Handler(fakeCore{exists: false})
	payload, _ := json.Marshal(queue.WarmupTickPayload{MailboxID: "missing"})
	task := asynq.NewTask(queue.TaskWarmupTick, payload)

	if err := h(context.Background(), task); err != nil {
		t.Fatalf("handler returned error for unknown mailbox: %v", err)
	}
}
