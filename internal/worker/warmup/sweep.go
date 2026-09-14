package warmup

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/fleetdecision"
	"github.com/inroad/inroad/internal/platform/metrics"
)

// recordRefusal appends a "this mailbox was not placed" entry to the fleet
// decision log, when the coreapi client offers that capability.
//
// It is feature-detected per call rather than threaded through SweepHandler's
// signature, for the same reason maintenance.Cleaner and deliverability.Breaker
// are: coreapi.Client already has ~40 methods and 13 fakes, and this is one call
// site. A client without it records nothing, which is exactly the behaviour that
// preceded the log.
//
// The write is best-effort by design. A decision that could not be logged is
// degraded observability; failing the sweep over it would turn a missing log line
// into a stalled warmup pool for every mailbox behind this one.
func recordRefusal(ctx context.Context, core coreapi.Client, mb coreapi.MailboxRef) {
	rec, ok := core.(coreapi.FleetDecisionRecorder)
	if !ok {
		return
	}
	// Forced, not Chose: no worker was scored, because strict band segregation
	// found none eligible to score. Rendering this as a comparison would invent a
	// runner-up and an operator would tune a threshold against a number nothing
	// computed.
	entry := fleetdecision.Entry{
		Kind: fleetdecision.KindRefused,
		// No WorkerID: a refusal placed the mailbox nowhere, and naming the
		// worker it was refused FROM would read as the one it landed on.
		MailboxID:   mb.ID,
		WorkspaceID: mb.WorkspaceID,
		Reason: fleetdecision.Forced(
			"strict risk-band segregation found no live worker already carrying this mailbox's band, " +
				"and no idle worker to adopt into it; add fleet capacity or wait for the mailbox's warmup lane to recover"),
		// The actor is the ASSIGNMENT attempt that refused, not a separate
		// "refuse" automation — there is no such thing, and naming one would
		// imply a component an operator could go and look at.
		TriggeredBy: fleetdecision.Auto(fleetdecision.KindAssign),
	}
	if err := rec.RecordFleetDecision(ctx, entry); err != nil {
		slog.WarnContext(ctx, "fleet decision not recorded",
			"kind", entry.Kind, "mailbox_id", mb.ID, "err", err)
	}
}

// sweepKind is the metric label for this sweep. Fixed, so the dimension stays
// bounded.
const sweepKind = "warmup"

// SweepHandler returns an asynq handler for warmup:sweep tasks. It fans out one
// warmup:tick per due participant — routing each to the FROM-mailbox's assigned
// worker queue so a mailbox's warmup and campaign mail egress from one IP
// (per-IP routing, spec §15; Dest is derived server-side from the assignment,
// never from client input) — then recomputes participant health.
//
// Idempotent: the tick's TaskID dedups a re-seed racing the send handler's lazy
// chain, and ClaimWarmupSend guards the actual send, so a duplicate fan-out
// never double-sends. A single mailbox's routing failure must not block the rest
// of the pool or the health pass, so per-mailbox failures are counted and logged
// (the sweep is retried on its 5-minute cadence), matching the enrollment
// sweeper; only ListDueWarmupMailboxes and EvaluateWarmupHealth abort the tick.
//
// mtx records the fan-out scan's duration and due-participant count.
// ListDueWarmupMailboxes is the other known-unbounded scan, so
// inroad_sweep_rows_total{kind="warmup"} is its growth curve; measuring is all
// this does. The window covers the LIST only, not the per-mailbox routing loop
// or EvaluateWarmupHealth, so the number stays comparable to the other sweeps'
// (all three measure "how much did the scan cost and return"). A nil mtx
// no-ops.
func SweepHandler(core coreapi.Client, enq Enqueuer, mtx *metrics.Metrics) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		started := time.Now()
		due, err := core.ListDueWarmupMailboxes(ctx)
		if err != nil {
			// See sequence.SweepHandler: a failed scan is not an observation.
			return err
		}
		mtx.SweepCompleted(sweepKind, len(due), time.Since(started))
		now := time.Now()
		var failures int
		for _, mb := range due {
			dest, err := core.AssignMailboxWorker(ctx, mb.ID, mb.WorkspaceID)
			if err != nil {
				failures++
				// A band-capacity refusal is a DECISION, not a failure: the
				// fleet chose not to co-locate this mailbox's traffic rather
				// than being unable to reach the database. It is the one case
				// here worth a decision-log entry, and it was previously
				// indistinguishable from a timeout — both landed in the same
				// anonymous `failures` counter, so a mailbox that stopped
				// warming because segregation had nowhere to put it looked
				// exactly like one whose assignment query timed out.
				if errors.Is(err, coreapi.ErrNoBandCapacity) {
					recordRefusal(ctx, core, mb)
				}
				continue
			}
			if err := enq.EnqueueWarmupTickAt(ctx, mb.ID, mb.WorkspaceID, now, dest); err != nil {
				failures++
			}
		}
		slog.Info("warmup_sweep", "due", len(due), "enqueue_failures", failures)
		return core.EvaluateWarmupHealth(ctx)
	}
}
