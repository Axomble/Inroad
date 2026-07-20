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

var _ coreapi.Client = fakeCore{}

func TestWarmupHandlerSkipsUnknownMailbox(t *testing.T) {
	h := Handler(fakeCore{exists: false})
	payload, _ := json.Marshal(queue.WarmupTickPayload{MailboxID: "missing"})
	task := asynq.NewTask(queue.TaskWarmupTick, payload)

	if err := h(context.Background(), task); err != nil {
		t.Fatalf("handler returned error for unknown mailbox: %v", err)
	}
}
