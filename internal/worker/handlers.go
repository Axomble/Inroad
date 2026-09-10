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

// Deps is everything Register wires onto the mux. It is a struct rather than a
// parameter list because the list had already reached thirteen and the role
// would have made it fourteen; the repo's own rule is an options struct past
// about three.
type Deps struct {
	// Role decides WHICH handlers are registered. See role.go for why the
	// distinction exists.
	Role Role

	Core     coreapi.Client
	Sender   *mail.MultiSender
	Engager  mail.Engager
	Reader   mail.InboxReader
	Enqueuer *queue.Client

	// Resolver is the DNS seam the domain-authentication sweep looks records up
	// through — injected at the composition root so tests never touch real DNS,
	// and MXResolver is the same seam for the recipient-domain ESP sweep's MX
	// lookups.
	Resolver   dnsauth.Resolver
	MXResolver esp.Resolver

	// PublicURL and TrackingSecret are threaded to the send handlers so they can
	// build/sign open and click tracking links (internal/worker/track) for
	// campaigns with tracking enabled. WarmupSecret lets the inbox poller verify
	// and isolate warmup mail (spec §7/§9.4).
	PublicURL      string
	TrackingSecret []byte
	WarmupSecret   []byte

	WebhookAllowPrivate bool

	// Metrics records inroad_sends_total at the campaign and warmup send
	// handlers' finalize points, inroad_sweep_seconds / inroad_sweep_rows_total
	// at the enrollment, inbox and warmup sweeps, and (via jobrun.Record below)
	// inroad_job_run_seconds at all six of the periodic reconciles in
	// cmd/worker/scheduler.go's sweepRegistrars(); a nil Metrics (metrics
	// disabled) no-ops throughout.
	Metrics *metrics.Metrics
}

// Register attaches this role's execution-plane handlers to the mux. A zero
// Role means everything: the predicates below normalise it (see role.go), so
// the easiest mistake at a composition root — a Deps literal that omits Role —
// yields today's single-process behaviour rather than a worker that registers
// nothing.
func Register(mux *asynq.ServeMux, d Deps) {
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
	recorder, _ := d.Core.(jobrun.Recorder)

	if d.Role.RunsScheduledWork() {
		registerScheduled(mux, d, recorder)
	}
	if d.Role.RunsPerMessageWork() {
		registerPerMessage(mux, d)
	}
}

// registerScheduled wires the six periodic reconciles and the campaign breaker.
// These scan or delete ACROSS TENANTS, which is why they are control-role only:
// a sending host must not be able to enumerate every workspace's due
// enrollments (fleet design §1.2).
func registerScheduled(mux *asynq.ServeMux, d Deps, recorder jobrun.Recorder) {
	if cleaner, ok := d.Core.(maintenance.Cleaner); ok {
		mux.HandleFunc(queue.TaskMaintenanceCleanup, jobrun.Record(recorder, d.Metrics, jobrun.NameMaintenanceCleanup, maintenance.CleanupHandler(cleaner)))
	}
	// Campaign circuit breaker. Registered by type assertion for the same reason
	// as the cleaner above: the capability is consumed through a one-method
	// interface rather than widening coreapi.Client (and its 13 test fakes) to
	// carry it. A Client that does not implement it simply has no breaker, which
	// is what a future HTTP coreapi would report until it grows the endpoint.
	//
	// Not one of the six jobrun.Record wraps: deliverability:evaluate is not in
	// cmd/worker/scheduler.go's sweepRegistrars() (it fires per-send-batch, not
	// on a fixed schedule), so it is outside this ledger's scope. It is still
	// control-plane work despite that: it reads campaign-wide aggregates and
	// pauses campaigns, which is a decision, not one message's delivery.
	if breaker, ok := d.Core.(deliverability.Breaker); ok {
		mux.HandleFunc(queue.TaskDeliverabilityEvaluate, deliverability.EvaluateHandler(breaker))
	}
	// Domain authentication: re-check stale sending domains' SPF/DKIM/DMARC.
	// Informational only — nothing on the send path reads the result.
	mux.HandleFunc(queue.TaskDomainAuthSweep, jobrun.Record(recorder, d.Metrics, jobrun.NameDomainAuthSweep,
		domainauth.SweepHandler(d.Core, d.Resolver, domainauth.DefaultStaleAfter)))
	// Recipient-domain ESP cache: classify by MX off the send path, and evict.
	// Registered by type assertion for the same reason as the cleaner/breaker —
	// the capability is consumed through recipientesp.Core rather than widening
	// coreapi.Client and its many fakes. A Client without it simply has no cache
	// refresh, which degrades to unmatched sending and never to a failed send.
	if rc, ok := d.Core.(recipientesp.Core); ok {
		mux.HandleFunc(queue.TaskRecipientESPSweep, jobrun.Record(recorder, d.Metrics, jobrun.NameRecipientESPSweep,
			recipientesp.SweepHandler(rc, d.MXResolver, recipientesp.DefaultStaleAfter, recipientesp.DefaultRetention)))
	}
	// Warmup: the fan-out/health sweep. Only warmup:sweep is one of the six
	// periodic reconciles; warmup:tick and warmup:engage are per-message
	// follow-ups, not scheduled sweeps, so they are outside the ledger the same
	// way warmup send/finalize is (see registerPerMessage).
	mux.HandleFunc(queue.TaskWarmupSweep, jobrun.Record(recorder, d.Metrics, jobrun.NameWarmupSweep, warmup.SweepHandler(d.Core, d.Enqueuer, d.Metrics)))
	// Multi-step sequencing: the reconcile. Only
	// sequence:sweep_stuck_enrollments (sweepRegistrars()' "enrollments") is
	// scheduled; sequence:advance is the per-enrollment lazy-chain step and is
	// outside the ledger for the same reason warmup:tick is.
	mux.HandleFunc(queue.TaskSweepEnrollments, jobrun.Record(recorder, d.Metrics, jobrun.NameEnrollments, sequence.SweepHandler(d.Core, d.Enqueuer, d.Metrics)))
	// Reply & bounce detection: the reconcile that fans out one inbox:poll per
	// mailbox. recorder is threaded through so inbox wraps inbox:sweep (its
	// scheduled reconcile) the same way every sweep here is wrapped; inbox:poll
	// and the manual-send handlers are per-message, not scheduled, and stay
	// unwrapped in registerPerMessage.
	inbox.RegisterScheduled(mux, d.Core, d.Enqueuer, d.Metrics, recorder)
}

// registerPerMessage wires the handlers that act on ONE message or one
// enrollment step. These are what a fleet host runs.
func registerPerMessage(mux *asynq.ServeMux, d Deps) {
	// Warmup: send one warmup email per tick (lazy chain) + recipient-side
	// engagement (rescue/mark-read/reply) of received warmup mail.
	mux.HandleFunc(queue.TaskWarmupTick, warmup.SendHandler(d.Core, d.Sender, d.Enqueuer, d.Metrics))
	mux.HandleFunc(queue.TaskWarmupEngage, warmup.EngageHandler(d.Core, d.Engager, d.Sender))
	// Test-send preview (POST /campaigns/{id}/test-send): registered by type
	// assertion for the same reason as the cleaner/breaker above -- the
	// capability (load raw step content + resolve a mailbox's decrypted
	// transport) is consumed through testsend.Core rather than widening
	// coreapi.Client. This is the ONLY place a mailbox credential is decrypted
	// or a provider is dialed for a test-send (docs/security.md invariant 1).
	if c, ok := d.Core.(testsend.Core); ok {
		mux.HandleFunc(queue.TaskTestSend, testsend.Handler(c, d.Sender))
	}
	// Multi-step sequencing: advance one step per task (lazy chain).
	mux.HandleFunc(queue.TaskSequenceAdvance, sequence.AdvanceHandler(d.Core, d.Sender, d.Enqueuer, d.PublicURL, d.TrackingSecret, d.Metrics))
	// Reply & bounce detection: poll one mailbox's INBOX per task.
	// WarmupSecret lets the poller verify + isolate warmup mail (spec §7/§9.4).
	inbox.RegisterPerMessage(mux, d.Core, d.Reader, d.Sender, d.Enqueuer, d.WarmupSecret)
	// Outbound webhooks: POST one signed delivery per task, retry on backoff.
	// Registered by type assertion for the same reason as the cleaner/breaker
	// above — the capability (load a delivery + open its endpoint secret) is
	// consumed through webhookworker.Core rather than widening coreapi.Client.
	if wc, ok := d.Core.(webhookworker.Core); ok {
		webhookworker.Register(mux, wc, d.Enqueuer, d.WebhookAllowPrivate)
	}
}
