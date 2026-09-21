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
)

// scheduledTasks, perMessageTasks and registered() moved to roletasks_test.go,
// which carries NO build tag, so the same three lists and the same routing
// question serve this file and the poolless role=send test beside it. Keeping
// two copies of "which tasks belong to which role" is how one of them stops
// being true.

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
