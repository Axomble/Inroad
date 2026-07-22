package inbox

import (
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// Register attaches the inbox:poll and inbox:sweep handlers to the mux.
func Register(mux *asynq.ServeMux, core coreapi.Client, reader mail.InboxReader, enq *queue.Client) {
	mux.HandleFunc(queue.TaskInboxPoll, PollHandler(core, reader))
	mux.HandleFunc(queue.TaskInboxSweep, SweepHandler(core, enq))
}
