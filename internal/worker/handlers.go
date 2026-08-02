// Package worker wires execution-plane task handlers onto an asynq mux.
package worker

import (
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/dnsauth"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/domainauth"
	"github.com/inroad/inroad/internal/worker/inbox"
	"github.com/inroad/inroad/internal/worker/maintenance"
	"github.com/inroad/inroad/internal/worker/sender"
	"github.com/inroad/inroad/internal/worker/sequence"
	"github.com/inroad/inroad/internal/worker/warmup"
)

// Register attaches all execution-plane handlers to the mux. publicURL and
// trackingSecret are threaded to the send handlers so they can build/sign
// open and click tracking links (internal/worker/track) for campaigns with
// tracking enabled. resolver is the DNS seam the domain-authentication sweep
// looks records up through — injected at the composition root so tests never
// touch real DNS.
func Register(mux *asynq.ServeMux, core coreapi.Client, sndr *mail.MultiSender, engager mail.Engager, reader mail.InboxReader, resolver dnsauth.Resolver, enq *queue.Client, publicURL string, trackingSecret, warmupSecret []byte) {
	if cleaner, ok := core.(maintenance.Cleaner); ok {
		mux.HandleFunc(queue.TaskMaintenanceCleanup, maintenance.CleanupHandler(cleaner))
	}
	// Domain authentication: re-check stale sending domains' SPF/DKIM/DMARC.
	// Informational only — nothing on the send path reads the result.
	mux.HandleFunc(queue.TaskDomainAuthSweep, domainauth.SweepHandler(core, resolver, domainauth.DefaultStaleAfter))
	// Warmup: send one warmup email per tick (lazy chain) + fan-out/health sweep +
	// recipient-side engagement (rescue/mark-read/reply) of received warmup mail.
	mux.HandleFunc(queue.TaskWarmupTick, warmup.SendHandler(core, sndr, enq))
	mux.HandleFunc(queue.TaskWarmupSweep, warmup.SweepHandler(core, enq))
	mux.HandleFunc(queue.TaskWarmupEngage, warmup.EngageHandler(core, engager, sndr))
	mux.HandleFunc(queue.TaskSendEmail, sender.Handler(core, sndr, enq, publicURL, trackingSecret))
	mux.HandleFunc(queue.TaskSweepStuck, sender.SweepStuckHandler(core, enq))
	// Multi-step sequencing: advance one step per task (lazy chain) + reconcile.
	mux.HandleFunc(queue.TaskSequenceAdvance, sequence.AdvanceHandler(core, sndr, enq, publicURL, trackingSecret))
	mux.HandleFunc(queue.TaskSweepEnrollments, sequence.SweepHandler(core, enq))
	// Reply & bounce detection: poll one mailbox's INBOX per task + reconcile.
	// warmupSecret lets the poller verify + isolate warmup mail (spec §7/§9.4).
	inbox.Register(mux, core, reader, enq, warmupSecret)
}
