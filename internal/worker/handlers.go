// Package worker wires execution-plane task handlers onto an asynq mux.
package worker

import (
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/dnsauth"
	"github.com/inroad/inroad/internal/platform/esp"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/deliverability"
	"github.com/inroad/inroad/internal/worker/domainauth"
	"github.com/inroad/inroad/internal/worker/inbox"
	"github.com/inroad/inroad/internal/worker/maintenance"
	"github.com/inroad/inroad/internal/worker/recipientesp"
	"github.com/inroad/inroad/internal/worker/sequence"
	"github.com/inroad/inroad/internal/worker/testsend"
	"github.com/inroad/inroad/internal/worker/warmup"
)

// Register attaches all execution-plane handlers to the mux. publicURL and
// trackingSecret are threaded to the send handlers so they can build/sign
// open and click tracking links (internal/worker/track) for campaigns with
// tracking enabled. resolver is the DNS seam the domain-authentication sweep
// looks records up through — injected at the composition root so tests never
// touch real DNS, and mxResolver is the same seam for the recipient-domain ESP
// sweep's MX lookups. mtx records inroad_sends_total at the campaign and warmup
// send handlers' finalize points; a nil mtx (metrics disabled) no-ops.
func Register(mux *asynq.ServeMux, core coreapi.Client, sndr *mail.MultiSender, engager mail.Engager, reader mail.InboxReader, resolver dnsauth.Resolver, mxResolver esp.Resolver, enq *queue.Client, publicURL string, trackingSecret, warmupSecret []byte, mtx *metrics.Metrics) {
	if cleaner, ok := core.(maintenance.Cleaner); ok {
		mux.HandleFunc(queue.TaskMaintenanceCleanup, maintenance.CleanupHandler(cleaner))
	}
	// Campaign circuit breaker. Registered by type assertion for the same reason
	// as the cleaner above: the capability is consumed through a one-method
	// interface rather than widening coreapi.Client (and its 13 test fakes) to
	// carry it. A Client that does not implement it simply has no breaker, which
	// is what a future HTTP coreapi would report until it grows the endpoint.
	if breaker, ok := core.(deliverability.Breaker); ok {
		mux.HandleFunc(queue.TaskDeliverabilityEvaluate, deliverability.EvaluateHandler(breaker))
	}
	// Domain authentication: re-check stale sending domains' SPF/DKIM/DMARC.
	// Informational only — nothing on the send path reads the result.
	mux.HandleFunc(queue.TaskDomainAuthSweep, domainauth.SweepHandler(core, resolver, domainauth.DefaultStaleAfter))
	// Recipient-domain ESP cache: classify by MX off the send path, and evict.
	// Registered by type assertion for the same reason as the cleaner/breaker —
	// the capability is consumed through recipientesp.Core rather than widening
	// coreapi.Client and its many fakes. A Client without it simply has no cache
	// refresh, which degrades to unmatched sending and never to a failed send.
	if rc, ok := core.(recipientesp.Core); ok {
		mux.HandleFunc(queue.TaskRecipientESPSweep, recipientesp.SweepHandler(
			rc, mxResolver, recipientesp.DefaultStaleAfter, recipientesp.DefaultRetention))
	}
	// Warmup: send one warmup email per tick (lazy chain) + fan-out/health sweep +
	// recipient-side engagement (rescue/mark-read/reply) of received warmup mail.
	mux.HandleFunc(queue.TaskWarmupTick, warmup.SendHandler(core, sndr, enq, mtx))
	mux.HandleFunc(queue.TaskWarmupSweep, warmup.SweepHandler(core, enq))
	mux.HandleFunc(queue.TaskWarmupEngage, warmup.EngageHandler(core, engager, sndr))
	// Test-send preview (POST /campaigns/{id}/test-send): registered by type
	// assertion for the same reason as the cleaner/breaker above -- the
	// capability (load raw step content + resolve a mailbox's decrypted
	// transport) is consumed through testsend.Core rather than widening
	// coreapi.Client. This is the ONLY place a mailbox credential is decrypted
	// or a provider is dialed for a test-send (docs/security.md invariant 1).
	if c, ok := core.(testsend.Core); ok {
		mux.HandleFunc(queue.TaskTestSend, testsend.Handler(c, sndr))
	}
	// Multi-step sequencing: advance one step per task (lazy chain) + reconcile.
	mux.HandleFunc(queue.TaskSequenceAdvance, sequence.AdvanceHandler(core, sndr, enq, publicURL, trackingSecret, mtx))
	mux.HandleFunc(queue.TaskSweepEnrollments, sequence.SweepHandler(core, enq))
	// Reply & bounce detection: poll one mailbox's INBOX per task + reconcile.
	// warmupSecret lets the poller verify + isolate warmup mail (spec §7/§9.4).
	inbox.Register(mux, core, reader, sndr, enq, warmupSecret)
}
