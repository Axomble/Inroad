package warmup

import (
	"context"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/metrics"
)

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
				continue
			}
			if err := enq.EnqueueWarmupTickAt(mb.ID, mb.WorkspaceID, now, dest); err != nil {
				failures++
			}
		}
		slog.Info("warmup_sweep", "due", len(due), "enqueue_failures", failures)
		return core.EvaluateWarmupHealth(ctx)
	}
}
