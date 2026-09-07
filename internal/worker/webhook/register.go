package webhook

import (
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/queue"
)

// Register attaches the webhook:deliver handler to the mux. Split out so
// worker.Register stays a flat list of one-liners.
func Register(mux *asynq.ServeMux, core Core, enq Enqueuer, allowPrivate bool) {
	mux.HandleFunc(queue.TaskWebhookDeliver, DeliverHandler(core, enq, allowPrivate))
}
