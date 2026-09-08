// Package worker wires execution-plane task handlers onto an asynq mux.
package worker

import (
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/dnsauth"
	"github.com/inroad/inroad/internal/platform/esp"
	"github.com/inroad/inroad/internal/platform/jobrun"
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
	webhookworker "github.com/inroad/inroad/internal/worker/webhook"
)

// Register attaches all execution-plane handlers to the mux. publicURL and
// trackingSecret are threaded to the send handlers so they can build/sign
// open and click tracking links (internal/worker/track) for campaigns with
// tracking enabled. resolver is the DNS seam the domain-authentication sweep
// looks records up through — injected at the composition root so tests never
// touch real DNS, and mxResolver is the same seam for the recipient-domain ESP
// sweep's MX lookups. mtx records inroad_sends_total at the campaign and warmup
// send handlers' finalize points, inroad_sweep_seconds / inroad_sweep_rows_total
// at the enrollment, inbox and warmup sweeps, and (via jobrun.Record below)
// inroad_job_run_seconds at all six of the periodic reconciles in
// cmd/worker/scheduler.go's sweepRegistrars(); a nil mtx (metrics disabled)
// no-ops throughout.
func Register(mux *asynq.ServeMux, core coreapi.Client, sndr *mail.MultiSender, engager mail.Engager, reader mail.InboxReader, resolver dnsauth.Resolver, mxResolver esp.Resolver, enq *queue.Client, publicURL string, trackingSecret, warmupSecret []byte, webhookAllowPrivate bool, mtx *metrics.Metrics) {
	// jobrun.Recorder is an optional coreapi capability, feature-detected the
	// same way as maintenance.Cleaner / deliverability.Breaker /
	// recipientesp.Core just below it: coreapi.Client already has 13 test
	// fakes, and widening it to carry a 41st method for one cross-cutting
	// concern would break every one of them for no gain. A Client that does
	// not implement it yields a nil recorder, and jobrun.Record treats nil as
	// "record nothing, run the sweep anyway" (see its own doc) — degraded
	// observability, never a failed sweep. Resolved once here, rather than at
	// each of the six call sites below, so there is exactly one assertion to
	// keep in sync with the Recorder interface.
	recorder, _ := core.(jobrun.Recorder)

	if cleaner, ok := core.(maintenance.Cleaner); ok {
		mux.HandleFunc(queue.TaskMaintenanceCleanup, jobrun.Record(recorder, mtx, jobrun.NameMaintenanceCleanup, maintenance.CleanupHandler(cleaner)))
	}
	// Campaign circuit breaker. Registered by type assertion for the same reason
	// as the cleaner above: the capability is consumed through a one-method
	// interface rather than widening coreapi.Client (and its 13 test fakes) to
	// carry it. A Client that does not implement it simply has no breaker, which
	// is what a future HTTP coreapi would report until it grows the endpoint.
	//
	// Not one of the six jobrun.Record wraps: deliverability:evaluate is not in
	// cmd/worker/scheduler.go's sweepRegistrars() (it fires per-send-batch, not
	// on a fixed schedule), so it is outside this ledger's scope.
	if breaker, ok := core.(deliverability.Breaker); ok {
		mux.HandleFunc(queue.TaskDeliverabilityEvaluate, deliverability.EvaluateHandler(breaker))
	}
	// Domain authentication: re-check stale sending domains' SPF/DKIM/DMARC.
	// Informational only — nothing on the send path reads the result.
	mux.HandleFunc(queue.TaskDomainAuthSweep, jobrun.Record(recorder, mtx, jobrun.NameDomainAuthSweep,
		domainauth.SweepHandler(core, resolver, domainauth.DefaultStaleAfter)))
	// Recipient-domain ESP cache: classify by MX off the send path, and evict.
	// Registered by type assertion for the same reason as the cleaner/breaker —
	// the capability is consumed through recipientesp.Core rather than widening
	// coreapi.Client and its many fakes. A Client without it simply has no cache
	// refresh, which degrades to unmatched sending and never to a failed send.
	if rc, ok := core.(recipientesp.Core); ok {
		mux.HandleFunc(queue.TaskRecipientESPSweep, jobrun.Record(recorder, mtx, jobrun.NameRecipientESPSweep,
			recipientesp.SweepHandler(rc, mxResolver, recipientesp.DefaultStaleAfter, recipientesp.DefaultRetention)))
	}
	// Warmup: send one warmup email per tick (lazy chain) + fan-out/health sweep +
	// recipient-side engagement (rescue/mark-read/reply) of received warmup mail.
	// Only warmup:sweep is one of the six periodic reconciles; warmup:tick and
	// warmup:engage are per-message follow-ups, not scheduled sweeps, so they
	// are outside the ledger the same way warmup send/finalize is.
	mux.HandleFunc(queue.TaskWarmupTick, warmup.SendHandler(core, sndr, enq, mtx))
	mux.HandleFunc(queue.TaskWarmupSweep, jobrun.Record(recorder, mtx, jobrun.NameWarmupSweep, warmup.SweepHandler(core, enq, mtx)))
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
	// Only sequence:sweep_stuck_enrollments (sweepRegistrars()' "enrollments") is
	// scheduled; sequence:advance is the per-enrollment lazy-chain step and is
	// outside the ledger for the same reason warmup:tick is.
	mux.HandleFunc(queue.TaskSequenceAdvance, sequence.AdvanceHandler(core, sndr, enq, publicURL, trackingSecret, mtx))
	mux.HandleFunc(queue.TaskSweepEnrollments, jobrun.Record(recorder, mtx, jobrun.NameEnrollments, sequence.SweepHandler(core, enq, mtx)))
	// Reply & bounce detection: poll one mailbox's INBOX per task + reconcile.
	// warmupSecret lets the poller verify + isolate warmup mail (spec §7/§9.4).
	// recorder is threaded through so inbox.Register can wrap ONLY inbox:sweep
	// (its scheduled reconcile) the same way every sweep here is wrapped;
	// inbox:poll and the manual-send handlers it also registers are per-message,
	// not scheduled, and stay unwrapped.
	inbox.Register(mux, core, reader, sndr, enq, warmupSecret, mtx, recorder)
	// Outbound webhooks: POST one signed delivery per task, retry on backoff.
	// Registered by type assertion for the same reason as the cleaner/breaker
	// above — the capability (load a delivery + open its endpoint secret) is
	// consumed through webhookworker.Core rather than widening coreapi.Client.
	if wc, ok := core.(webhookworker.Core); ok {
		webhookworker.Register(mux, wc, enq, webhookAllowPrivate)
	}
}
