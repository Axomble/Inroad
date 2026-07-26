package inbox

import (
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// Register attaches the inbox:poll and inbox:sweep handlers to the mux. The
// Gmail and Graph (m365) readers are constructed here (neither needs config —
// each provider's API host is fixed, so there is no SSRF flag to thread) and
// dispatched to per-mailbox by provider inside PollHandler. warmupSecret verifies
// the X-Inroad-Warmup receipt token so the poller can isolate warmup mail from
// campaign classification (spec §7/§9.4); enq schedules the warmup:engage
// follow-up when a warmup receipt is detected.
func Register(mux *asynq.ServeMux, core coreapi.Client, reader mail.InboxReader, enq *queue.Client, warmupSecret []byte) {
	// New(nil): Layer 3 (the optional model) is UNWIRED — there is no AI
	// provider yet, so a matched reply is classified by the deterministic,
	// offline Layer 1 (headers) + Layer 2 (lexicon) only.
	classifier := replyclassify.New(nil)
	mux.HandleFunc(queue.TaskInboxPoll, PollHandler(core, reader, mail.NewGmailReader(), mail.NewGraphReader(), classifier, warmupSecret, enq))
	mux.HandleFunc(queue.TaskInboxSweep, SweepHandler(core, enq))
}
