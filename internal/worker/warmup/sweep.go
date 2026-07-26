package warmup

import (
	"context"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

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
func SweepHandler(core coreapi.Client, enq Enqueuer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		due, err := core.ListDueWarmupMailboxes(ctx)
		if err != nil {
			return err
		}
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
