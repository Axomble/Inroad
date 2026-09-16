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
	// dests records the destination queue each poll was routed to, keyed by
	// mailbox — the per-IP affinity the sweep resolves before enqueuing.
	dests map[string]string
}

func (f *fakeEnqueuer) EnqueueInboxPoll(_ context.Context, mailboxID, _, dest string) error {
	if f.fail[mailboxID] {
		return errors.New("boom")
	}
	f.enqueued = append(f.enqueued, mailboxID)
	if f.dests == nil {
		f.dests = map[string]string{}
	}
	f.dests[mailboxID] = dest
	return nil
}

// sweepCore drives the sweep handler: a mailbox list from stubCore plus a
// per-mailbox worker assignment. Mirrors warmup's sweepCore, because the two
// sweeps now resolve routing the same way.
type sweepCore struct {
	stubCore
	// assign maps mailbox id -> queue AssignMailboxWorker hands back. A missing
	// entry resolves to "" — coreapi's sentinel for "no live assignment", which
	// must fall through to the shared send queue rather than strand the poll.
	assign map[string]string
	// assignErr, when set, fails EVERY resolve — the placement refusal
	// (coreapi.ErrNoEligibleWorker) and the transient-DB case.
	assignErr  error
	assignSeen []string
}

func (c *sweepCore) AssignMailboxWorker(_ context.Context, mailboxID, _ string) (string, error) {
	c.assignSeen = append(c.assignSeen, mailboxID)
	if c.assignErr != nil {
		return "", c.assignErr
	}
	return c.assign[mailboxID], nil
}

// TestSweepRoutesEachPollToItsMailboxAssignedWorker is the point of the
// affinity change: a poll authenticates to the mailbox's provider from the
// worker that runs it, so it must run on the worker that mailbox already
// authenticates from — not on whichever host happens to dequeue the shared
// queue first.
func TestSweepRoutesEachPollToItsMailboxAssignedWorker(t *testing.T) {
	core := &sweepCore{
		stubCore: stubCore{mailboxes: []coreapi.MailboxRef{
			{ID: "m1", WorkspaceID: "w1"},
			{ID: "m2", WorkspaceID: "w2"},
		}},
		assign: map[string]string{"m1": "w:host-a", "m2": "w:host-b"},
	}
	enq := &fakeEnqueuer{}
	if err := SweepHandler(core, enq, nil)(context.Background(), asynq.NewTask("inbox:sweep", nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := enq.dests["m1"]; got != "w:host-a" {
		t.Errorf("m1 routed to %q, want %q", got, "w:host-a")
	}
	if got := enq.dests["m2"]; got != "w:host-b" {
		t.Errorf("m2 routed to %q, want %q", got, "w:host-b")
	}
}

// TestSweepLeavesAnUnassignedMailboxOnTheSharedQueue is the self-host and
// first-poll case: no assignment means no IP to stay on, so the poll keeps the
// shared-queue behaviour it has always had. A single-process RoleAll install
// never assigns anything through this path and must be unaffected.
func TestSweepLeavesAnUnassignedMailboxOnTheSharedQueue(t *testing.T) {
	core := &sweepCore{
		stubCore: stubCore{mailboxes: []coreapi.MailboxRef{{ID: "m1", WorkspaceID: "w1"}}},
	}
	enq := &fakeEnqueuer{}
	if err := SweepHandler(core, enq, nil)(context.Background(), asynq.NewTask("inbox:sweep", nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(enq.enqueued) != 1 {
		t.Fatalf("enqueued %v, want exactly one poll", enq.enqueued)
	}
	if got := enq.dests["m1"]; got != "" {
		t.Errorf("unassigned mailbox routed to %q, want \"\" (the enqueuer's role-queue fallback)", got)
	}
}

// TestSweepSkipsAMailboxItCannotRoute proves a routing failure costs ONE
// mailbox one interval, not the whole tick. It mirrors warmup's sweep: a
// refusal (coreapi.ErrNoEligibleWorker — every live worker is blocked or
// unreachable for this provider) or a transient resolve error skips that
// mailbox, and the next sweep three minutes later retries it.
func TestSweepSkipsAMailboxItCannotRoute(t *testing.T) {
	core := &sweepCore{
		stubCore:  stubCore{mailboxes: []coreapi.MailboxRef{{ID: "m1", WorkspaceID: "w1"}}},
		assignErr: coreapi.ErrNoEligibleWorker,
	}
	enq := &fakeEnqueuer{}
	if err := SweepHandler(core, enq, nil)(context.Background(), asynq.NewTask("inbox:sweep", nil)); err != nil {
		t.Fatalf("a per-mailbox routing failure must not fail the sweep, got %v", err)
	}
	if len(enq.enqueued) != 0 {
		t.Errorf("enqueued %v, want none — an unroutable mailbox waits for the next sweep", enq.enqueued)
	}
	if len(core.assignSeen) != 1 {
		t.Errorf("AssignMailboxWorker called for %v, want exactly m1", core.assignSeen)
	}
}

// TestSweepEnqueuesOnePollPerActiveMailbox drives the fan-out happy path:
// every active mailbox gets exactly one inbox:poll task.
func TestSweepEnqueuesOnePollPerActiveMailbox(t *testing.T) {
	core := &sweepCore{stubCore: stubCore{mailboxes: []coreapi.MailboxRef{{ID: "m1", WorkspaceID: "w1"}, {ID: "m2", WorkspaceID: "w1"}}}}
	enq := &fakeEnqueuer{}
	h := SweepHandler(core, enq, nil)
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
	core := &sweepCore{stubCore: stubCore{mailboxes: []coreapi.MailboxRef{{ID: "m1", WorkspaceID: "w1"}, {ID: "m2", WorkspaceID: "w1"}}}}
	enq := &fakeEnqueuer{fail: map[string]bool{"m1": true}}
	h := SweepHandler(core, enq, nil)
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
	core := &sweepCore{stubCore: stubCore{listErr: want}}
	enq := &fakeEnqueuer{}
	h := SweepHandler(core, enq, nil)
	if err := h(context.Background(), asynq.NewTask("inbox:sweep", nil)); !errors.Is(err, want) {
		t.Fatalf("expected core error to propagate, got %v", err)
	}
}
