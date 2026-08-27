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

// Enqueuer schedules an inbox:poll task. Satisfied by *queue.Client.
type Enqueuer interface {
	EnqueueInboxPoll(mailboxID, workspaceID string) error
}

// SweepHandler returns an asynq handler for inbox:sweep tasks: it fans out
// one inbox:poll task per active mailbox. Mirrors sequence.SweepHandler's
// tolerant-of-partial-failure shape.
//
// mtx records the scan's duration and mailbox count. ListActiveMailboxes is
// one of the known-unbounded scans: it returns EVERY active mailbox in the
// installation on every tick, so inroad_sweep_rows_total{kind="inbox"} is the
// growth curve to watch. Measuring it is all this does — the scan itself is
// tracked and bounded separately. A nil mtx no-ops.
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
			if err := enq.EnqueueInboxPoll(m.ID, m.WorkspaceID); err != nil {
				failures++
			}
		}
		slog.Info("inbox_sweep", "mailboxes", len(mailboxes), "enqueue_failures", failures)
		return nil
	}
}
