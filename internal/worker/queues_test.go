package worker

import (
	"slices"
	"testing"

	"github.com/inroad/inroad/internal/platform/queue"
)

func TestQueuesForControlNeverConsumesAQueuePerMessageWorkLandsOn(t *testing.T) {
	// THE property this whole slice exists for. A control host that consumes
	// "send" or "default" can claim a task it has no handler for; asynq
	// dequeues before consulting the mux, so it burns the retries and
	// dead-letters the task. Mail is lost that way.
	got := QueuesFor(RoleControl, "abc")
	for _, forbidden := range []string{queue.QueueSend, queue.QueueDefault, queue.WorkerQueue("abc")} {
		if slices.Contains(got, forbidden) {
			t.Errorf("control role consumes %q; got %v", forbidden, got)
		}
	}
	if !slices.Contains(got, queue.QueueControl) {
		t.Errorf("control role does not consume %q; got %v", queue.QueueControl, got)
	}
}

func TestQueuesForSendConsumesItsOwnQueueFirstThenSendThenDefault(t *testing.T) {
	// Order is priority: queuePriorities weights earlier queues higher, so the
	// per-mailbox affinity queue must come first or a busy shared queue starves
	// the warmup ticks that depend on sending from a stable IP.
	want := []string{queue.WorkerQueue("abc"), queue.QueueSend, queue.QueueDefault}
	if got := QueuesFor(RoleSend, "abc"); !slices.Equal(got, want) {
		t.Errorf("QueuesFor(send) = %v, want %v", got, want)
	}
}

func TestQueuesForAllConsumesEverythingSoNoTaskBecomesUnreachable(t *testing.T) {
	got := QueuesFor(RoleAll, "abc")
	for _, required := range []string{
		queue.WorkerQueue("abc"), queue.QueueSend, queue.QueueControl, queue.QueueDefault,
	} {
		if !slices.Contains(got, required) {
			t.Errorf("RoleAll does not consume %q — a single-process install would silently stop running it; got %v", required, got)
		}
	}
}

func TestQueuesForWithoutAWorkerIDOmitsTheAffinityQueue(t *testing.T) {
	// An empty worker id would produce the queue "w:", which nothing enqueues
	// to — consuming it is dead weight and makes the priority weights lie.
	for _, role := range []Role{RoleAll, RoleSend} {
		for _, q := range QueuesFor(role, "") {
			if q == "w:" {
				t.Errorf("%s with no worker id consumes the empty affinity queue %q", role, q)
			}
		}
	}
}

func TestQueuesForZeroValueRoleMatchesRoleAll(t *testing.T) {
	// role.go's predicates normalise "" to all; this must agree or a
	// zero-value Deps consumes a different set than it registers.
	if !slices.Equal(QueuesFor(Role(""), "abc"), QueuesFor(RoleAll, "abc")) {
		t.Error("zero-value Role does not match RoleAll")
	}
}
