package sender

import (
	"context"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

// SendEnqueuer is the subset of *queue.Client the sweeper needs. Defined at
// the consumer so tests can inject a fake without pulling in the concrete
// asynq-backed client. workspaceID accompanies sendID so re-enqueued tasks
// carry the pin the worker will use in its DB lookups.
type SendEnqueuer interface {
	EnqueueSend(sendID, workspaceID string) error
}

// SweepStuckHandler returns an asynq handler that re-enqueues sends stuck in
// 'queued' longer than the reconcile window. It is the failure-recovery half
// of the launch pipeline: campaign.Service.Launch commits DB rows even when
// Redis enqueue fails, so this periodic sweep is what turns those orphaned
// rows back into live tasks.
//
// The handler is idempotent: re-enqueuing a send that's already being worked
// is a no-op at the DB layer (SetSendResult writes a terminal status), and a
// second enqueue costs at most a duplicate asynq task the send handler
// harmlessly re-processes.
func SweepStuckHandler(core coreapi.Client, enq SendEnqueuer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		rows, err := core.ListStuckQueuedSends(ctx)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var failures int
		for _, row := range rows {
			if err := enq.EnqueueSend(row.SendID, row.WorkspaceID); err != nil {
				failures++
			}
		}
		slog.Info("sweep_stuck", "candidates", len(rows), "reenqueue_failures", failures)
		return nil
	}
}
