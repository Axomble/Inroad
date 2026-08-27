package sequence

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
const sweepKind = "enrollments"

// SweepHandler re-enqueues active enrollments whose next_due_at passed the
// reconcile window without a live advance task (launch committed rows but Redis
// enqueue failed, or a scheduled task was lost). It is the failure-recovery
// half of the lazy chain.
//
// Idempotent: a duplicate advance is harmless — GetStepSendJob no-ops on a
// stopped/completed enrollment (Skip), and delivery is guarded by the
// claim-before-send (ClaimStepSend): a re-driven step whose sends row is already
// 'sending'/'sent' loses the claim and skips the send rather than double-sending.
// mtx records the sweep's duration and candidate-row count
// (inroad_sweep_seconds / inroad_sweep_rows_total) so the scan's growth is
// visible; a nil mtx no-ops.
func SweepHandler(core coreapi.Client, enq Enqueuer, mtx *metrics.Metrics) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		started := time.Now()
		rows, err := core.ListDueEnrollments(ctx)
		if err != nil {
			// Deliberately unrecorded: a failed scan has no row count, and
			// folding its (short) duration into the histogram would drag the
			// sweep's apparent cost DOWN during exactly the incident an
			// operator is looking at.
			return err
		}
		mtx.SweepCompleted(sweepKind, len(rows), time.Since(started))
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
