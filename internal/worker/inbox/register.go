package inbox

import (
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// Register attaches the inbox:poll and inbox:sweep handlers to the mux. The
// Gmail reader is constructed here (it needs no config — the Google API host is
// fixed, so there is no SSRF flag to thread) and dispatched to per-mailbox by
// provider inside PollHandler.
func Register(mux *asynq.ServeMux, core coreapi.Client, reader mail.InboxReader, enq *queue.Client) {
	mux.HandleFunc(queue.TaskInboxPoll, PollHandler(core, reader, mail.NewGmailReader()))
	mux.HandleFunc(queue.TaskInboxSweep, SweepHandler(core, enq))
}
