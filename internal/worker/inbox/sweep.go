package inbox

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
const sweepKind = "inbox"

// Enqueuer schedules an inbox:poll task, routed to the polled mailbox's
// assigned worker queue. Satisfied by *queue.Client.
type Enqueuer interface {
	EnqueueInboxPoll(ctx context.Context, mailboxID, workspaceID, dest string) error
}

// SweepHandler returns an asynq handler for inbox:sweep tasks: it fans out
// one inbox:poll task per active mailbox, routing each to THAT mailbox's
// assigned worker queue so it keeps authenticating to its provider from one
// egress IP (Dest is derived server-side from the assignment, never from client
// input). Mirrors warmup.SweepHandler, which resolves routing the same way, and
// sequence.SweepHandler's tolerant-of-partial-failure shape.
//
// A mailbox that cannot be routed is SKIPPED for this tick rather than polled
// from an arbitrary worker. The two reasons AssignMailboxWorker fails are a
// transient database error and coreapi.ErrNoEligibleWorker — every live worker
// blocked or unreachable for this mailbox's provider, in which case the poll
// would have failed from any of them anyway. Either way the cost is bounded by
// inboxSweepInterval: the next tick, a few minutes later, retries. Note this is
// NOT the unassigned case — AssignMailboxWorker returns ("", nil) when there is
// no live assignment, and the enqueuer then falls back to the shared send queue,
// which is what keeps a single-process self-host install working unchanged.
//
// mtx records the scan's duration and mailbox count. ListActiveMailboxes is
// one of the known-unbounded scans: it returns EVERY active mailbox in the
// installation on every tick, so inroad_sweep_rows_total{kind="inbox"} is the
// growth curve to watch. Measuring it is all this does — the scan itself is
// tracked and bounded separately. The window covers the LIST only, not the
// per-mailbox routing loop, so the number stays comparable to the other sweeps'.
// A nil mtx no-ops.
func SweepHandler(core coreapi.Client, enq Enqueuer, mtx *metrics.Metrics) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		started := time.Now()
		mailboxes, err := core.ListActiveMailboxes(ctx)
		if err != nil {
			// See sequence.SweepHandler: a failed scan is not an observation.
			return err
		}
		mtx.SweepCompleted(sweepKind, len(mailboxes), time.Since(started))
		var failures int
		for _, m := range mailboxes {
			// No decision-log entry on failure, for the reason warmup.SweepHandler
			// gives: only the placement path knows WHY it refused, and it writes
			// that entry itself where the decision is made.
			dest, err := core.AssignMailboxWorker(ctx, m.ID, m.WorkspaceID)
			if err != nil {
				failures++
				continue
			}
			if err := enq.EnqueueInboxPoll(ctx, m.ID, m.WorkspaceID, dest); err != nil {
				failures++
			}
		}
		slog.Info("inbox_sweep", "mailboxes", len(mailboxes), "enqueue_failures", failures)
		return nil
	}
}
