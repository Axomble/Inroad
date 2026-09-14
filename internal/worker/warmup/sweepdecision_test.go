package warmup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/fleetdecision"
	"github.com/inroad/inroad/internal/platform/queue"
)

// recordingSweepCore is sweepCore plus the optional decision-recorder
// capability, so these tests exercise the branch that only exists when the
// coreapi client implements it.
type recordingSweepCore struct {
	sweepCore

	entries []fleetdecision.Entry
	recErr  error
}

func (c *recordingSweepCore) RecordFleetDecision(_ context.Context, e fleetdecision.Entry) error {
	c.entries = append(c.entries, e)
	return c.recErr
}

// Strict risk-band segregation declining to place a mailbox is a DECISION, and
// before the decision log it was invisible: the sweep counted it as an anonymous
// failure alongside a timeout and moved on, so an operator watching a degraded
// mailbox go quiet had nothing to read.
func TestSweepRecordsABandCapacityRefusal(t *testing.T) {
	core := &recordingSweepCore{sweepCore: sweepCore{
		due:       []coreapi.MailboxRef{{ID: "mb-1", WorkspaceID: "ws-1"}},
		assignErr: coreapi.ErrNoBandCapacity,
	}}
	enq := &fakeEnq{}

	if err := SweepHandler(core, enq, nil)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(core.entries) != 1 {
		t.Fatalf("recorded %d decisions, want 1: %+v", len(core.entries), core.entries)
	}
	got := core.entries[0]
	if got.Kind != fleetdecision.KindRefused {
		t.Errorf("kind = %q, want %q", got.Kind, fleetdecision.KindRefused)
	}
	if got.MailboxID != "mb-1" || got.WorkspaceID != "ws-1" {
		t.Errorf("entry names mailbox %q in workspace %q, want mb-1 / ws-1", got.MailboxID, got.WorkspaceID)
	}
	// A refusal has no destination, so naming one would be a claim nothing made.
	if got.WorkerID != "" {
		t.Errorf("worker id = %q, want empty — a refusal placed the mailbox nowhere", got.WorkerID)
	}
	if got.TriggeredBy != fleetdecision.Auto(fleetdecision.KindAssign) {
		t.Errorf("triggered_by = %q, want %q — the actor is the assignment attempt that refused",
			got.TriggeredBy, fleetdecision.Auto(fleetdecision.KindAssign))
	}
	if err := got.Validate(); err != nil {
		t.Errorf("the recorded entry is not writable: %v", err)
	}
	// No comparison: nothing was scored, because no candidate was eligible.
	if strings.Contains(got.Reason.String(), " over ") {
		t.Errorf("reason = %q, which reads as a score comparison that never happened", got.Reason)
	}
}

// Only a typed band-capacity refusal is a decision. A timeout or a database blip
// is a FAILURE — recording it as "the fleet decided not to place this mailbox"
// would put a lie in the log that an operator would then act on.
func TestSweepDoesNotRecordAnOrdinaryAssignmentFailure(t *testing.T) {
	core := &recordingSweepCore{sweepCore: sweepCore{
		due:       []coreapi.MailboxRef{{ID: "mb-1", WorkspaceID: "ws-1"}},
		assignErr: context.DeadlineExceeded,
	}}

	if err := SweepHandler(core, &fakeEnq{}, nil)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(core.entries) != 0 {
		t.Fatalf("recorded %d decisions for a transport failure, want 0: %+v", len(core.entries), core.entries)
	}
}

// A decision that could not be logged is degraded observability. It must not
// fail the sweep, and it must not stop the rest of the pool being processed or
// the health pass from running.
func TestSweepSurvivesAFailingDecisionRecorder(t *testing.T) {
	core := &recordingSweepCore{
		sweepCore: sweepCore{
			due:       []coreapi.MailboxRef{{ID: "mb-1", WorkspaceID: "ws-1"}, {ID: "mb-2", WorkspaceID: "ws-2"}},
			assignErr: coreapi.ErrNoBandCapacity,
		},
		recErr: errors.New("database is down"),
	}

	if err := SweepHandler(core, &fakeEnq{}, nil)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("a failed decision log must not fail the sweep, got %v", err)
	}
	if len(core.entries) != 2 {
		t.Errorf("attempted %d decision writes, want one per refused mailbox", len(core.entries))
	}
	if !core.evaluated {
		t.Error("EvaluateWarmupHealth must still run after a decision-log failure")
	}
}

// A coreapi client without the capability collects no decisions and must behave
// exactly as it did before the log existed.
func TestSweepWithoutTheRecorderCapabilityStillRuns(t *testing.T) {
	core := &sweepCore{
		due:       []coreapi.MailboxRef{{ID: "mb-1", WorkspaceID: "ws-1"}},
		assignErr: coreapi.ErrNoBandCapacity,
	}
	if err := SweepHandler(core, &fakeEnq{}, nil)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !core.evaluated {
		t.Error("EvaluateWarmupHealth must still run")
	}
}
