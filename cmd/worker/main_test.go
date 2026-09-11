package main

import (
	"slices"
	"testing"

	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker"
)

// TestWorkerConsumesByRoleWhenNoOverrideIsSet proves that, absent an operator
// override, the queue set a worker consumes comes from its ROLE
// (worker.QueuesFor) — control gets only the control queue, send gets its
// affinity queue plus the shared send/default queues. Before this, every role
// consumed the same {"w:<id>", "default"} pair, which is the role-blindness
// that made the send/control split inoperable.
func TestWorkerConsumesByRoleWhenNoOverrideIsSet(t *testing.T) {
	for _, tc := range []struct {
		role worker.Role
		want []string
	}{
		{worker.RoleControl, []string{queue.QueueControl}},
		{worker.RoleSend, []string{queue.WorkerQueue("w1"), queue.QueueSend, queue.QueueDefault}},
	} {
		got := resolveWorkerQueues(&config.Config{}, tc.role, "w1")
		if !slices.Equal(got, tc.want) {
			t.Errorf("role %s: got %v, want %v", tc.role, got, tc.want)
		}
	}
}

// TestAnExplicitQueueOverrideWinsEntirely proves INROAD_WORKER_QUEUES (surfaced
// as config.Config.WorkerQueues) replaces the role default ENTIRELY rather than
// supplementing it. An operator who names queues means exactly those — silently
// appending the role's defaults would make the setting useless for isolating a
// host.
func TestAnExplicitQueueOverrideWinsEntirely(t *testing.T) {
	cfg := &config.Config{WorkerQueues: []string{"only-this"}}
	if got := resolveWorkerQueues(cfg, worker.RoleSend, "w1"); !slices.Equal(got, []string{"only-this"}) {
		t.Errorf("override did not win: got %v", got)
	}
}
