package main

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/jobrun"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker"
	"github.com/inroad/inroad/internal/worker/fleet"
	workerinbox "github.com/inroad/inroad/internal/worker/inbox"
	"github.com/inroad/inroad/internal/worker/maintenance"
	"github.com/inroad/inroad/internal/worker/recipientesp"
)

// scheduledSweeps pairs every periodic reconcile's asynq task type with the
// ledger name it must be recorded under. It is the only place the two
// vocabularies meet: sweepRegistrars() knows the names and platform/queue knows
// the task types, and nothing before this test connected them.
var scheduledSweeps = map[string]string{
	queue.TaskSweepEnrollments:      jobrun.NameEnrollments,
	queue.TaskInboxSweep:            jobrun.NameInboxSweep,
	queue.TaskWarmupSweep:           jobrun.NameWarmupSweep,
	queue.TaskMaintenanceCleanup:    jobrun.NameMaintenanceCleanup,
	queue.TaskMaintenanceRetention:  jobrun.NameRetention,
	queue.TaskDomainAuthSweep:       jobrun.NameDomainAuthSweep,
	queue.TaskRecipientESPSweep:     jobrun.NameRecipientESPSweep,
	queue.TaskFleetRotate:           jobrun.NameFleetRotate,
	queue.TaskInboxPendingSendSweep: jobrun.NameInboxPendingSweep,
}

// recordingCore is the coreapi client worker.Register wires the sweeps over.
//
// coreapi.Client is EMBEDDED as a nil interface (the shape inbox's stubCore
// uses) so the 35 methods no sweep calls cost no boilerplate — and would panic
// loudly if one were reached, which is the assertion: a periodic reconcile
// should do nothing here but be counted. The methods that ARE overridden are
// exactly the sweeps' entry points, each answering "nothing due", plus the
// five capability interfaces Register resolves by type assertion
// (maintenance.Cleaner, maintenance.Retainer, recipientesp.Core, fleet.Rotator
// and — reached via inbox.RegisterScheduled — inbox.PendingSweepCore). Without those the handlers
// are never registered at all and ProcessTask would report "handler not found",
// which is itself a failure this test should catch.
type recordingCore struct {
	coreapi.Client
	runs []jobrun.Run
}

// RecordJobRun is the capability under test: internal/worker/handlers.go
// resolves it with `core.(jobrun.Recorder)` and degrades silently to nil.
func (c *recordingCore) RecordJobRun(_ context.Context, run jobrun.Run) error {
	c.runs = append(c.runs, run)
	return nil
}

// --- the scheduled sweeps' entry points, all answering "nothing due" ---

func (c *recordingCore) ListDueEnrollments(context.Context) ([]coreapi.DueEnrollment, error) {
	return nil, nil
}
func (c *recordingCore) ListActiveMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return nil, nil
}
func (c *recordingCore) ListDueWarmupMailboxes(context.Context) ([]coreapi.MailboxRef, error) {
	return nil, nil
}
func (c *recordingCore) EvaluateWarmupHealth(context.Context) error { return nil }
func (c *recordingCore) ListStaleSendingDomains(context.Context, time.Duration) ([]coreapi.SendingDomainRef, error) {
	return nil, nil
}

// --- maintenance.Cleaner (resolved by type assertion in Register) ---

func (c *recordingCore) CleanupExpired(context.Context) (int64, error)          { return 0, nil }
func (c *recordingCore) PurgeIdempotencyKeys(context.Context) (int64, error)    { return 0, nil }
func (c *recordingCore) PurgeWarmupObservations(context.Context) (int64, error) { return 0, nil }
func (c *recordingCore) PurgeDeadWorkers(context.Context) (int64, error)        { return 0, nil }
func (c *recordingCore) PurgeWebhookDeliveries(context.Context) (int64, error)  { return 0, nil }
func (c *recordingCore) PurgeScheduledJobRuns(context.Context) (int64, error)   { return 0, nil }
func (c *recordingCore) PurgeWorkerProviderSignals(context.Context) (int64, error) {
	return 0, nil
}
func (c *recordingCore) PurgeFleetDecisions(context.Context) (int64, error) { return 0, nil }

// --- maintenance.Retainer (resolved by type assertion in Register) ---
//
// Each answers "nothing aged out". The Deps below enable every table, so the
// retention handler really does call each of these — a sweep that registered
// but skipped every table would record a row while exercising nothing.

func (c *recordingCore) PurgeDeliverabilityEvents(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return coreapi.RetentionBatch{}, nil
}
func (c *recordingCore) PurgeInboxThreads(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return coreapi.RetentionBatch{}, nil
}
func (c *recordingCore) RollupTrackingEvents(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return coreapi.RetentionBatch{}, nil
}
func (c *recordingCore) PurgeSends(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return coreapi.RetentionBatch{}, nil
}
func (c *recordingCore) PurgeDeadLetters(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return coreapi.RetentionBatch{}, nil
}

// --- recipientesp.Core (resolved by type assertion in Register) ---

func (c *recordingCore) PurgeExpiredRecipientDomains(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (c *recordingCore) ListStaleRecipientDomains(context.Context, time.Duration) ([]coreapi.RecipientDomainRef, error) {
	return nil, nil
}
func (c *recordingCore) RecordRecipientDomainESP(context.Context, coreapi.RecipientDomainESP) error {
	return nil
}

// --- fleet.Rotator (resolved by type assertion in Register) ---

func (c *recordingCore) RotateMailboxWorkers(context.Context) (int64, error) { return 0, nil }

// --- inbox.PendingSweepCore (resolved by type assertion in RegisterScheduled) ---

func (c *recordingCore) ListStrandedPendingInboxSends(context.Context, coreapi.StrandedPendingWindow) ([]coreapi.StrandedPendingSend, error) {
	return nil, nil
}

var (
	_ coreapi.Client               = (*recordingCore)(nil)
	_ jobrun.Recorder              = (*recordingCore)(nil)
	_ maintenance.Cleaner          = (*recordingCore)(nil)
	_ maintenance.Retainer         = (*recordingCore)(nil)
	_ recipientesp.Core            = (*recordingCore)(nil)
	_ fleet.Rotator                = (*recordingCore)(nil)
	_ workerinbox.PendingSweepCore = (*recordingCore)(nil)
)

// Every periodic reconcile must land exactly one row in the run ledger, under
// the name sweepRegistrars() schedules it by.
//
// The property used to be asserted by comparing sweepRegistrars() to the
// jobrun.Name* constants, which proved less than it read: both sides of that
// comparison are the constants, and the THIRD side — internal/worker/handlers.go,
// where the jobrun.Record wrapping actually happens — was never touched. Deleting
// a wrap left it green. So this drives the real thing: the real Register builds
// the mux, and each of the scheduled task types is dispatched through it. asynq's
// ServeMux is itself an asynq.Handler, so ProcessTask exercises registration and
// routing with no Redis.
//
// It catches all three ways the ledger can go quiet: an unwrapped handler (no
// row), a handler Register never registered ("handler not found"), and a name
// that does not match the schedule (wrong row).
func TestEverySweepDispatchedThroughRegisterRecordsOneLedgerRow(t *testing.T) {
	ctx := context.Background()
	core := &recordingCore{}

	mux := queue.NewMux()
	// nil senders/readers/resolvers/enqueuer: a sweep with nothing due reaches
	// none of them, and a nil dereference here would mean a reconcile did more
	// than scan-and-count. jobrun.Record re-panics, so it would fail loudly.
	worker.Register(mux, worker.Deps{Role: worker.RoleAll, Core: core, PublicURL: "https://app.test",
		Retention: maintenance.RetentionPolicy{
			DeliverabilityEvents: 90 * 24 * time.Hour, InboxThreads: 30 * 24 * time.Hour,
			TrackingEvents: 30 * 24 * time.Hour, Sends: 90 * 24 * time.Hour, DeadLetters: 90 * 24 * time.Hour,
		}})

	for taskType, wantName := range scheduledSweeps {
		t.Run(taskType, func(t *testing.T) {
			before := len(core.runs)
			if err := mux.ProcessTask(ctx, asynq.NewTask(taskType, nil)); err != nil {
				t.Fatalf("dispatching %s through the real mux: %v", taskType, err)
			}
			got := core.runs[before:]
			if len(got) != 1 {
				t.Fatalf("%s recorded %d ledger rows, want exactly 1 — an unwrapped "+
					"jobrun.Record in internal/worker/handlers.go is invisible everywhere else", taskType, len(got))
			}
			if got[0].Name != wantName {
				t.Errorf("%s recorded as %q, want %q (the name sweepRegistrars() schedules it by)",
					taskType, got[0].Name, wantName)
			}
			if got[0].Outcome != jobrun.OutcomeOK {
				t.Errorf("%s outcome = %q (%s), want ok for a sweep with nothing due",
					taskType, got[0].Outcome, got[0].ErrorMessage)
			}
		})
	}

	if len(core.runs) != len(scheduledSweeps) {
		t.Fatalf("recorded %d rows in total, want %d", len(core.runs), len(scheduledSweeps))
	}
}

// The two vocabularies must describe the same set of sweeps. Kept alongside the
// dispatch test above rather than folded into it: this one fails when another
// sweep is scheduled but not added to scheduledSweeps, which would otherwise
// leave the new sweep's wrapping unasserted while everything stayed green.
func TestScheduledSweepsCoverEveryRegistrar(t *testing.T) {
	want := make([]string, 0, len(scheduledSweeps))
	for _, name := range scheduledSweeps {
		want = append(want, name)
	}
	got := make([]string, 0, len(scheduledSweeps))
	for _, s := range sweepRegistrars() {
		got = append(got, s.name)
	}
	sort.Strings(want)
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("sweepRegistrars() = %v, scheduledSweeps = %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("sweepRegistrars() = %v, scheduledSweeps = %v", got, want)
		}
	}
}
