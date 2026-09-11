package worker

import "github.com/inroad/inroad/internal/platform/queue"

// QueuesFor is the ordered set of asynq queues a worker of this role consumes.
// Order is priority: queue.queuePriorities weights earlier entries higher.
//
// This lives here rather than in platform/config because it is role logic, and
// platform must not import internal/worker. Config still owns the operator
// override (INROAD_WORKER_QUEUES); this is the default it overrides.
func QueuesFor(role Role, workerID string) []string {
	var qs []string
	// The affinity queue first, so a busy shared queue cannot starve the
	// warmup ticks routed here. Omitted without a worker id: "w:" is a queue
	// nothing enqueues to, and consuming it would only skew the weights.
	if role.RunsPerMessageWork() && workerID != "" {
		qs = append(qs, queue.WorkerQueue(workerID))
	}
	if role.RunsPerMessageWork() {
		qs = append(qs, queue.QueueSend)
	}
	if role.RunsScheduledWork() {
		qs = append(qs, queue.QueueControl)
	}
	// Transitional drain, and deliberately NOT for the control role: a control
	// host that consumes default can claim the per-message backlog it has no
	// handler for, which is the exact failure this slice removes.
	if role.RunsPerMessageWork() {
		qs = append(qs, queue.QueueDefault)
	}
	return qs
}
