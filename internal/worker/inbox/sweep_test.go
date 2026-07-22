package inbox

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

type fakeEnqueuer struct {
	fail     map[string]bool
	enqueued []string
}

func (f *fakeEnqueuer) EnqueueInboxPoll(mailboxID, _ string) error {
	if f.fail[mailboxID] {
		return errors.New("boom")
	}
	f.enqueued = append(f.enqueued, mailboxID)
	return nil
}

// TestSweepEnqueuesOnePollPerActiveMailbox drives the fan-out happy path:
// every active mailbox gets exactly one inbox:poll task.
func TestSweepEnqueuesOnePollPerActiveMailbox(t *testing.T) {
	core := &stubCore{mailboxes: []coreapi.MailboxRef{{ID: "m1", WorkspaceID: "w1"}, {ID: "m2", WorkspaceID: "w1"}}}
	enq := &fakeEnqueuer{}
	h := SweepHandler(core, enq)
	if err := h(context.Background(), asynq.NewTask("inbox:sweep", nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(enq.enqueued) != 2 {
		t.Fatalf("expected 2 enqueues, got %d (%v)", len(enq.enqueued), enq.enqueued)
	}
}

// TestSweepTolerantOfPartialEnqueueFailure guards the reconcile-under-redis-
// blip case: one bad enqueue must not fail the whole sweep tick.
func TestSweepTolerantOfPartialEnqueueFailure(t *testing.T) {
	core := &stubCore{mailboxes: []coreapi.MailboxRef{{ID: "m1", WorkspaceID: "w1"}, {ID: "m2", WorkspaceID: "w1"}}}
	enq := &fakeEnqueuer{fail: map[string]bool{"m1": true}}
	h := SweepHandler(core, enq)
	if err := h(context.Background(), asynq.NewTask("inbox:sweep", nil)); err != nil {
		t.Fatalf("expected sweep to swallow enqueue failure, got: %v", err)
	}
	if len(enq.enqueued) != 1 || enq.enqueued[0] != "m2" {
		t.Fatalf("expected only m2 enqueued, got %v", enq.enqueued)
	}
}

// TestSweepPropagatesCoreError proves the handler surfaces core-side errors
// (so asynq can retry the sweep) rather than silently no-op'ing.
func TestSweepPropagatesCoreError(t *testing.T) {
	want := errors.New("db down")
	core := &stubCore{listErr: want}
	enq := &fakeEnqueuer{}
	h := SweepHandler(core, enq)
	if err := h(context.Background(), asynq.NewTask("inbox:sweep", nil)); !errors.Is(err, want) {
		t.Fatalf("expected core error to propagate, got %v", err)
	}
}
