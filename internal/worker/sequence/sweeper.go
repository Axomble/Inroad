package sequence

import (
	"context"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

// SweepHandler re-enqueues active enrollments whose next_due_at passed the
// reconcile window without a live advance task (launch committed rows but Redis
// enqueue failed, or a scheduled task was lost). It is the failure-recovery
// half of the lazy chain.
//
// Idempotent: a duplicate advance is harmless — GetStepSendJob no-ops on a
// stopped/completed enrollment (Skip), and the (campaign, contact, step_order)
// unique index added in migration 000008 is the backstop: RecordStepSend's ON
// CONFLICT makes a re-sent step a no-op on the sends row rather than a duplicate.
func SweepHandler(core coreapi.Client, enq Enqueuer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		rows, err := core.ListDueEnrollments(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var failures int
		for _, r := range rows {
			// Re-enqueue immediately; the enrollment is already past due.
			if err := enq.EnqueueAdvanceIn(r.EnrollmentID, r.WorkspaceID, 0); err != nil {
				failures++
			}
		}
		slog.Info("sweep_stuck_enrollments", "candidates", len(rows), "reenqueue_failures", failures)
		return nil
	}
}
