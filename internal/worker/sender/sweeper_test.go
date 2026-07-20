package sender

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

// fakeCore implements coreapi.Client for the sweeper test — only the two
// methods the sweeper actually uses need real behavior.
type fakeCore struct {
	stuck []string
	err   error
}

func (f *fakeCore) MailboxExists(context.Context, string) (bool, error) { return false, nil }
func (f *fakeCore) GetSendJob(context.Context, string) (coreapi.SendJob, error) {
	return coreapi.SendJob{}, nil
}
func (f *fakeCore) MarkSend(context.Context, string, coreapi.SendResult) error { return nil }
func (f *fakeCore) ListStuckQueuedSends(context.Context) ([]string, error) {
	return f.stuck, f.err
}

type fakeSendEnqueuer struct {
	fail     map[string]bool
	enqueued []string
}

func (f *fakeSendEnqueuer) EnqueueSend(sendID string) error {
	if f.fail[sendID] {
		return errors.New("boom")
	}
	f.enqueued = append(f.enqueued, sendID)
	return nil
}

// TestSweepStuckReenqueuesAll drives the reconcile happy path: everything the
// core reports as stuck gets pushed back onto the queue exactly once.
func TestSweepStuckReenqueuesAll(t *testing.T) {
	core := &fakeCore{stuck: []string{"a", "b", "c"}}
	enq := &fakeSendEnqueuer{}
	h := SweepStuckHandler(core, enq)
	if err := h(context.Background(), asynq.NewTask("send:sweep_stuck", nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(enq.enqueued) != 3 {
		t.Fatalf("expected 3 re-enqueues, got %d (%v)", len(enq.enqueued), enq.enqueued)
	}
}

// TestSweepStuckTolerantOfPartialEnqueueFailure guards the reconcile-under-
// redis-blip case: the handler must not error out on one bad enqueue, else
// the whole periodic tick fails and the remaining ids never get retried.
func TestSweepStuckTolerantOfPartialEnqueueFailure(t *testing.T) {
	core := &fakeCore{stuck: []string{"a", "b", "c"}}
	enq := &fakeSendEnqueuer{fail: map[string]bool{"b": true}}
	h := SweepStuckHandler(core, enq)
	if err := h(context.Background(), asynq.NewTask("send:sweep_stuck", nil)); err != nil {
		t.Fatalf("expected sweep to swallow enqueue failure, got: %v", err)
	}
	if len(enq.enqueued) != 2 {
		t.Fatalf("expected 2 successful re-enqueues, got %d", len(enq.enqueued))
	}
}

// TestSweepStuckPropagatesCoreError proves the handler surfaces core-side
// errors (so asynq can retry the sweep) rather than silently no-op'ing.
func TestSweepStuckPropagatesCoreError(t *testing.T) {
	want := errors.New("db down")
	core := &fakeCore{err: want}
	enq := &fakeSendEnqueuer{}
	h := SweepStuckHandler(core, enq)
	if err := h(context.Background(), asynq.NewTask("send:sweep_stuck", nil)); !errors.Is(err, want) {
		t.Fatalf("expected core error to propagate, got %v", err)
	}
}
