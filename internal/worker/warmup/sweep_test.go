package warmup

import (
	"context"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

// sweepCore drives the sweep handler: a fixed due list, a per-mailbox worker
// assignment ("w:"+mailbox), and a record of whether health eval ran.
type sweepCore struct {
	stubCore

	due        []coreapi.MailboxRef
	assignErr  error
	evaluated  bool
	assignSeen []string
}

func (c *sweepCore) ListDueWarmupMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return c.due, nil
}
func (c *sweepCore) AssignMailboxWorker(_ context.Context, mailboxID, _ string) (string, error) {
	c.assignSeen = append(c.assignSeen, mailboxID)
	if c.assignErr != nil {
		return "", c.assignErr
	}
	return "w:" + mailboxID, nil
}
func (c *sweepCore) EvaluateWarmupHealth(context.Context) error {
	c.evaluated = true
	return nil
}

func TestSweepFansOutPerMailboxWithAssignedDestAndEvaluatesHealth(t *testing.T) {
	core := &sweepCore{due: []coreapi.MailboxRef{
		{ID: "mb-1", WorkspaceID: "ws-1"},
		{ID: "mb-2", WorkspaceID: "ws-2"},
	}}
	enq := &fakeEnq{}

	if err := SweepHandler(core, enq, nil)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if len(enq.calls) != 2 {
		t.Fatalf("enqueue calls = %d, want 2 (one per due mailbox)", len(enq.calls))
	}
	// Each tick routes to the FROM-mailbox's assigned worker queue (per-IP routing).
	want := map[string]tickCall{
		"mb-1": {mailboxID: "mb-1", workspaceID: "ws-1", dest: "w:mb-1"},
		"mb-2": {mailboxID: "mb-2", workspaceID: "ws-2", dest: "w:mb-2"},
	}
	for _, got := range enq.calls {
		w, ok := want[got.mailboxID]
		if !ok {
			t.Fatalf("unexpected enqueue for %q", got.mailboxID)
		}
		if got.workspaceID != w.workspaceID || got.dest != w.dest {
			t.Fatalf("tick %q = %+v, want ws=%s dest=%s", got.mailboxID, got, w.workspaceID, w.dest)
		}
	}
	if !core.evaluated {
		t.Fatalf("EvaluateWarmupHealth was not called")
	}
}

func TestSweepStillEvaluatesHealthWhenAssignFails(t *testing.T) {
	core := &sweepCore{
		due:       []coreapi.MailboxRef{{ID: "mb-1", WorkspaceID: "ws-1"}},
		assignErr: context.DeadlineExceeded,
	}
	enq := &fakeEnq{}

	if err := SweepHandler(core, enq, nil)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("a per-mailbox routing failure must not fail the sweep, got %v", err)
	}
	if len(enq.calls) != 0 {
		t.Fatalf("no tick should enqueue when assignment fails, got %d", len(enq.calls))
	}
	// One bad mailbox must not block the health pass for the rest of the pool.
	if !core.evaluated {
		t.Fatalf("EvaluateWarmupHealth must still run after a per-mailbox failure")
	}
}

func TestSweepStillEvaluatesHealthWhenEnqueueFails(t *testing.T) {
	core := &sweepCore{due: []coreapi.MailboxRef{
		{ID: "mb-1", WorkspaceID: "ws-1"},
		{ID: "mb-2", WorkspaceID: "ws-2"},
	}}
	// mb-1's tick enqueue fails; mb-2 must still be processed.
	enq := &fakeEnq{failOn: "mb-1", err: context.DeadlineExceeded}

	if err := SweepHandler(core, enq, nil)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("a per-mailbox enqueue failure must not fail the sweep, got %v", err)
	}
	// Both mailboxes are attempted; the failure on mb-1 doesn't short-circuit mb-2.
	if len(enq.calls) != 2 {
		t.Fatalf("enqueue attempts = %d, want 2 (the failure must not skip the rest)", len(enq.calls))
	}
	seen := map[string]bool{}
	for _, c := range enq.calls {
		seen[c.mailboxID] = true
	}
	if !seen["mb-1"] || !seen["mb-2"] {
		t.Fatalf("expected both mb-1 and mb-2 attempted, saw %v", seen)
	}
	// The health pass still runs for the pool despite the per-mailbox failure.
	if !core.evaluated {
		t.Fatalf("EvaluateWarmupHealth must still run after a per-mailbox enqueue failure")
	}
}

func TestSweepEmptyPoolStillEvaluatesHealth(t *testing.T) {
	core := &sweepCore{}
	enq := &fakeEnq{}

	if err := SweepHandler(core, enq, nil)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(enq.calls) != 0 {
		t.Fatalf("empty pool should enqueue nothing, got %d", len(enq.calls))
	}
	if !core.evaluated {
		t.Fatalf("EvaluateWarmupHealth must run even with an empty pool")
	}
}
