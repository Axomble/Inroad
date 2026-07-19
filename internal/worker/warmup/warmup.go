// Package warmup is the execution-plane ramp engine (v1: no-op pacing tick).
package warmup

import (
	"context"
	"encoding/json"

	"github.com/hibiken/asynq"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

// Handler returns an asynq handler for warmup:tick tasks.
func Handler(core coreapi.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.WarmupTickPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		ok, err := core.MailboxExists(ctx, p.MailboxID)
		if err != nil {
			return err
		}
		if !ok {
			return nil // mailbox gone; nothing to pace
		}
		// v1: ramp logic is a no-op placeholder. Real pacing lands with the mailbox domain.
		return nil
	}
}
