package sender

import (
	"context"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

// SendEnqueuer is the subset of *queue.Client the sweeper needs. Defined at
// the consumer so tests can inject a fake without pulling in the concrete
// asynq-backed client.
type SendEnqueuer interface {
	EnqueueSend(sendID string) error
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
		ids, err := core.ListStuckQueuedSends(ctx)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		var failures int
		for _, id := range ids {
			if err := enq.EnqueueSend(id); err != nil {
				failures++
			}
		}
		slog.Info("sweep_stuck", "candidates", len(ids), "reenqueue_failures", failures)
		return nil
	}
}
