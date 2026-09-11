//go:build integration

package worker

// The role split is invisible to a unit test: every handler is registered on the
// same mux, and a handler that should NOT be registered simply isn't there. The
// only way to observe it is to ask the mux which task types it routes, which is
// what TestRegisterWiresTheDeliverabilityBreaker established — and it takes the
// REAL in-process coreapi client to ask honestly, because most of these
// handlers are wired behind a type assertion that every fake in the repo
// satisfies whether Register wires it or not. Hence the integration tag: the
// client needs a pool, though this test issues no query of its own.
//
// This matters more than a normal wiring test: if a sweep leaks into the send
// role, a host we do not trust silently gains the ability to enumerate every
// workspace's due enrollments, and nothing else in the suite would notice.

import (
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/queue"
)

// scheduledTasks are the six periodic reconciles plus the breaker: control-role
// only. Keep in sync with cmd/worker/scheduler.go sweepRegistrars().
var scheduledTasks = []string{
	queue.TaskMaintenanceCleanup,
	queue.TaskDomainAuthSweep,
	queue.TaskRecipientESPSweep,
	queue.TaskWarmupSweep,
	queue.TaskSweepEnrollments,
	queue.TaskInboxSweep,
	queue.TaskDeliverabilityEvaluate,
}

// perMessageTasks are the handlers a fleet host runs. Together with
// scheduledTasks above this is EVERY Task* constant in platform/queue: an
// unlisted constant is a handler whose role nothing asserts, which is how the
// drain-only inbox:reply_send below went uncovered.
var perMessageTasks = []string{
	queue.TaskWarmupTick,
	queue.TaskWarmupEngage,
	queue.TaskTestSend,
	queue.TaskSequenceAdvance,
	queue.TaskInboxPoll,
	// Deprecated and drain-only — nothing enqueues it any more — but
	// inbox.RegisterPerMessage still registers it behind ReplyCore so tasks
	// already in Redis at cutover get delivered. That makes it per-message work
	// like any other: a control host must not claim it, and a send host must
	// still drain it. Delete this line with the constant, its handler and
	// ReplyCore, not before.
	//nolint:staticcheck // SA1019: asserting the DRAIN registration's role is the point.
	queue.TaskInboxReplySend,
	queue.TaskInboxPendingReplySend,
	queue.TaskInboxPendingComposeSend,
	queue.TaskWebhookDeliver,
}

// registered reports whether mux routes taskType. asynq's ServeMux answers that
// without running anything: Handler returns the matched pattern, or an empty
// one alongside its NotFoundHandler for a type nothing claims. That is the real
// routing table, resolved by asynq's own longest-prefix rules — the same lookup
// ProcessTask does one line before it invokes.
//
// It asks instead of dispatching, which is the one place this test departs from
// TestRegisterWiresTheDeliverabilityBreaker below. There, dispatching is the
// point: one task, fixtures seeded for it, an effect asserted afterwards. Here
// the mux carries every handler at once over the shared integration database
// and the assertion is only "is it routed", so dispatching all fifteen buys
// nothing and costs a great deal. Measured, not assumed: dispatching
// maintenance:cleanup purged 72 idempotency keys, 20 dead letters and 3 worker
// assignments belonging to other packages' tests, which run concurrently under
// `go test -p 4`, and domainauth:sweep then panicked in dnsauth.lookup because
// the nil Resolver these Deps carry meets whatever sending domains the rest of
// the suite has left in the database.
func registered(t *testing.T, mux *asynq.ServeMux, taskType string) bool {
	t.Helper()
	_, pattern := mux.Handler(asynq.NewTask(taskType, nil))
	return pattern != ""
}

func TestSendRoleRegistersNoScheduledWork(t *testing.T) {
	pool := registrationPool(t)
	core := registrationCore(t, pool)
	mux := asynq.NewServeMux()
	Register(mux, Deps{Role: RoleSend, Core: core, PublicURL: "https://app.test"})

	for _, tt := range scheduledTasks {
		if registered(t, mux, tt) {
			t.Errorf("send role registered %q — a fleet host must not run cross-tenant scans or purges", tt)
		}
	}
	for _, tt := range perMessageTasks {
		if !registered(t, mux, tt) {
			t.Errorf("send role did not register %q — a fleet host cannot do its job without it", tt)
		}
	}
}

func TestControlRoleRegistersNoPerMessageWork(t *testing.T) {
	pool := registrationPool(t)
	core := registrationCore(t, pool)
	mux := asynq.NewServeMux()
	Register(mux, Deps{Role: RoleControl, Core: core, PublicURL: "https://app.test"})

	for _, tt := range scheduledTasks {
		if !registered(t, mux, tt) {
			t.Errorf("control role did not register %q", tt)
		}
	}
	for _, tt := range perMessageTasks {
		if registered(t, mux, tt) {
			t.Errorf("control role registered %q — per-message work belongs on the send role", tt)
		}
	}
}

func TestRoleAllIsUnchangedFromTodaysBehaviour(t *testing.T) {
	pool := registrationPool(t)
	core := registrationCore(t, pool)
	mux := asynq.NewServeMux()
	Register(mux, Deps{Role: RoleAll, Core: core, PublicURL: "https://app.test"})

	for _, tt := range append(append([]string{}, scheduledTasks...), perMessageTasks...) {
		if !registered(t, mux, tt) {
			t.Errorf("RoleAll did not register %q — the default must be exactly today's behaviour", tt)
		}
	}
}

// A Deps literal that omits Role must behave as RoleAll, matching ParseRole's
// rule that an unset INROAD_WORKER_ROLE means today's behaviour. Without this,
// the zero value of the struct — the easiest possible mistake at a composition
// root — satisfies neither predicate and yields a worker that starts, reports
// healthy, consumes its queues and runs NOTHING.
func TestRoleAllIsTheZeroValueOfDeps(t *testing.T) {
	pool := registrationPool(t)
	core := registrationCore(t, pool)
	mux := asynq.NewServeMux()
	Register(mux, Deps{Core: core, PublicURL: "https://app.test"})

	for _, tt := range append(append([]string{}, scheduledTasks...), perMessageTasks...) {
		if !registered(t, mux, tt) {
			t.Errorf("a Deps with no Role did not register %q — an unset role must mean everything", tt)
		}
	}
}
