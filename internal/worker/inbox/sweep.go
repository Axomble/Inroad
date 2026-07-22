package inbox

import (
	"context"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
)

// Enqueuer schedules an inbox:poll task. Satisfied by *queue.Client.
type Enqueuer interface {
	EnqueueInboxPoll(mailboxID, workspaceID string) error
}

// SweepHandler returns an asynq handler for inbox:sweep tasks: it fans out
// one inbox:poll task per active mailbox. Mirrors sequence.SweepHandler's
// tolerant-of-partial-failure shape.
func SweepHandler(core coreapi.Client, enq Enqueuer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		mailboxes, err := core.ListActiveMailboxes(ctx)
		if err != nil {
			return err
		}
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
