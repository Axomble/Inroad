package inbox

import (
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// Register attaches this domain's task handlers to the mux. The Gmail and Graph
// (m365) readers are constructed here (neither needs config — each provider's
// API host is fixed, so there is no SSRF flag to thread) and dispatched to
// per-mailbox by provider inside PollHandler. warmupSecret verifies the
// X-Inroad-Warmup receipt token so the poller can isolate warmup mail from
// campaign classification (spec §7/§9.4); enq schedules the warmup:engage
// follow-up when a warmup receipt is detected. sender delivers manual replies
// queued from the unified inbox — the SAME *mail.MultiSender every other send
// path uses. mtx records the inbox sweep's duration and mailbox count; a nil
// mtx no-ops.
func Register(mux *asynq.ServeMux, core coreapi.Client, reader mail.InboxReader, sender Mailer, enq *queue.Client, warmupSecret []byte, mtx *metrics.Metrics) {
	// New(nil): Layer 3 (the optional model) is UNWIRED — there is no AI
	// provider yet, so a matched reply is classified by the deterministic,
	// offline Layer 1 (headers) + Layer 2 (lexicon) only.
	classifier := replyclassify.New(nil)
	mux.HandleFunc(queue.TaskInboxPoll, PollHandler(core, reader, mail.NewGmailReader(), mail.NewGraphReader(), classifier, warmupSecret, enq))
	mux.HandleFunc(queue.TaskInboxSweep, SweepHandler(core, enq, mtx))
	// DRAIN ONLY. Nothing enqueues an inbox:reply_send any more — every manual
	// reply, immediate or deferred, is now an inbox_pending_replies row and the
	// pointer task below. This registration stays for one release so tasks
	// already in Redis at cutover still get delivered, and is removed together
	// with ReplySendHandler, ReplyCore, queue.TaskInboxReplySend and
	// queue.InboxReplySendPayload. See ReplySendHandler's doc for the signal
	// that says the queue has drained.
	if rc, ok := core.(ReplyCore); ok {
		//nolint:staticcheck // SA1019: registering the deprecated task type IS the
		// drain; without this registration the in-flight tasks would never run.
		mux.HandleFunc(queue.TaskInboxReplySend, ReplySendHandler(rc, sender))
	}
	// Manual replies, both paths: the task names an inbox_pending_replies row
	// and the handler reads the body from it, so no correspondence travels in a
	// payload. Registered by type assertion for the same reason as testsend.Core
	// — the capability is consumed through PendingReplyCore rather than by
	// widening coreapi.Client. This is the ONLY place a mailbox credential is
	// decrypted or a provider is dialed for a manual reply (docs/security.md
	// invariant 1).
	if pc, ok := core.(PendingReplyCore); ok {
		mux.HandleFunc(queue.TaskInboxPendingReplySend, PendingReplySendHandler(pc, sender))
	}
	// Deferred composed (non-reply) emails.
	if cc, ok := core.(ComposeCore); ok {
		mux.HandleFunc(queue.TaskInboxPendingComposeSend, PendingComposeSendHandler(cc, sender))
	}
}
