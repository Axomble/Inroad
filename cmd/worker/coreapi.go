package main

import (
	"errors"
	"log/slog"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/worker"
)

// coreAPIMode is where this worker's relational reads come from. It is the
// second of the two security-relevant decisions in this binary — the first is
// credentialMode next door — and it is separated from the wiring for the same
// reason: a decision worth testing is a decision worth being able to test
// without a database, a Redis or a network.
type coreAPIMode int

const (
	// coreAPILocal: the process reads through its own pgxpool, in-process.
	// This is EXACTLY the behaviour that shipped before the remote transport
	// existed, it is what every self-hosted installation runs, and it is what a
	// worker that sets no new variable gets.
	coreAPILocal coreAPIMode = iota
	// coreAPIRemote: the process asks the control plane over the fleet channel
	// for the methods the transport has taken over — after slice 3b, the one
	// suppression check, the eight per-message job READS, the twenty claim and
	// outcome WRITES, and the twelve manual reply/compose calls. The pool is
	// still open for everything else (the inbox poll cursor, the inbound-message
	// store, the warmup receipt, and every periodic sweep); see
	// internal/coreapi/remote's package doc for what that does and does not buy
	// yet.
	coreAPIRemote
)

// coreAPIRemoteMethods is what this worker reaches remotely, logged at startup
// so an operator can see the boundary move slice by slice rather than having to
// read the source to find out what the flag currently covers.
const coreAPIRemoteMethods = "IsSuppressed, GetStepSendJob, GetInboxPollJob, GetWarmupSendJob, " +
	"GetWarmupEngageJob, GetWebhookDeliveryJob, GetTestSendContent, ResolveSenderTransport, FindSendByMessageID, " +
	"ClaimStepSend, MarkStepDelivered, AdvanceStepCursor, ReleaseStepSend, FinalizeStepSend, MarkStepStopped, " +
	"DeferEnrollment, IncrementEnrollmentCapDeferrals, ClaimWarmupSend, MarkWarmupSent, ReleaseWarmupSend, " +
	"FailWarmupSend, MarkWarmupEngaged, MarkReplied, RecordReplyClass, MarkUnsubscribed, MarkBounced, " +
	"MarkWebhookDelivered, MarkWebhookRetrying, MarkWebhookFailed, " +
	"GetInboxReplyJob, RecordInboxReply, ClaimInboxReply, ReleaseInboxReply, " +
	"ClaimPendingInboxReply, MarkPendingInboxReplySent, ReleasePendingInboxReply, FailPendingInboxReply, " +
	"ClaimPendingInboxCompose, MarkPendingInboxComposeSent, ReleasePendingInboxCompose, FailPendingInboxCompose"

func (m coreAPIMode) String() string {
	if m == coreAPIRemote {
		return "remote"
	}
	return "in-process"
}

// ErrCoreAPIRemoteNeedsSendRole refuses the flag on any role but send. A
// control-role worker runs beside the API and a role=all worker IS the
// single-process self-host topology — in both, the database is right there, so
// a network hop to reach it would be pure latency. Refusing is not pedantry:
// an operator who sets this believes their worker stopped reading the tenant
// database, and silently ignoring it would leave that belief uncorrected.
var ErrCoreAPIRemoteNeedsSendRole = errors.New(
	"INROAD_FLEET_COREAPI_REMOTE applies only to a role=send worker: a control or all-role worker runs beside the database it would be asking the control plane about")

// ErrCoreAPIRemoteNeedsFleetURL is the fail-closed half: a worker told to read
// remotely with nowhere to read from refuses at startup rather than starting
// and failing every check — and never falls back to the local pool, which is
// the one outcome that would make the flag a lie.
var ErrCoreAPIRemoteNeedsFleetURL = errors.New(
	"INROAD_FLEET_COREAPI_REMOTE needs INROAD_FLEET_BROKER_URL (and INROAD_FLEET_BROKER_TOKEN): the coreapi transport shares the credential broker's listener and token")

// resolveCoreAPIMode decides where this process reads from, from the role and
// the configuration alone.
//
// The matrix, stated once:
//
//	role     flag  url  →  outcome
//	any      no    any  →  in-process   (the default; self-host, unchanged)
//	send     yes   yes  →  remote
//	send     yes   no   →  ERROR (nowhere to read from — fail closed)
//	control  yes   any  →  ERROR (the flag does nothing there)
//	all      yes   any  →  ERROR (same)
func resolveCoreAPIMode(cfg *config.Config, role worker.Role) (coreAPIMode, error) {
	if !cfg.FleetCoreAPIRemote {
		return coreAPILocal, nil
	}
	if role != worker.RoleSend {
		return coreAPILocal, ErrCoreAPIRemoteNeedsSendRole
	}
	if cfg.FleetBrokerURL == "" {
		return coreAPILocal, ErrCoreAPIRemoteNeedsFleetURL
	}
	return coreAPIRemote, nil
}

// ErrCoreAPIRemoteNeedsBroker is the third fail-closed refusal, and it names a
// dependency between the two decisions this binary makes. Reading coreapi
// remotely means the job responses carry no credential — a fleet worker gets
// those from the credential broker, so that one channel stays the only thing in
// the installation handing out a plaintext secret. Without a broker there would
// be a job and nothing to send it with.
//
// In practice resolveCredentialMode has already refused a role=send worker
// without a broker (ErrSendRoleNeedsBroker), and role=send is the only role
// this flag is allowed on — so this is the belt-and-braces half, checked here
// because the ORDER of two independent resolutions is not something the next
// person to touch this file should have to reason about.
var ErrCoreAPIRemoteNeedsBroker = errors.New(
	"INROAD_FLEET_COREAPI_REMOTE needs the credential broker: coreapi job responses carry no credential, and a worker reading them remotely obtains one through INROAD_FLEET_BROKER_URL")

// coreAPIWiring is what the composition root got back. client is non-nil only
// in coreAPIRemote, and it satisfies ALL FOUR inprocess source interfaces
// (SuppressionSource, JobSource, OutcomeSource, InboxSendSource) — one transport,
// one set of connection pools, one token.
type coreAPIWiring struct {
	mode   coreAPIMode
	client *remote.Client
}

// buildCoreAPIWiring turns an ALREADY-RESOLVED mode into the concrete
// dependency. It is the only place cmd/worker builds a remote coreapi client.
//
// The mode is a parameter rather than resolved here because the two halves have
// to happen at different points in the composition root: resolveCoreAPIMode is
// pure and runs before anything connects, so a role/flag combination that
// cannot work fails with no database attempt behind it; this half needs the
// credential broker, which is built after the pool.
//
// creds is that broker. The coreapi client takes it because a job response
// carries no credential and the client fills one in per job; it is nil in every
// mode but credentialsBrokered, which is why remote mode refuses without it.
func buildCoreAPIWiring(cfg *config.Config, mode coreAPIMode, creds credbroker.Opener, logger *slog.Logger) (coreAPIWiring, error) {
	if mode != coreAPIRemote {
		return coreAPIWiring{mode: mode}, nil
	}
	if creds == nil {
		return coreAPIWiring{}, ErrCoreAPIRemoteNeedsBroker
	}
	client, err := remote.NewClient(cfg.FleetBrokerURL, cfg.FleetBrokerToken, cfg.FleetBrokerAllowPlaintext, creds)
	if err != nil {
		return coreAPIWiring{}, err
	}
	logger.Info("coreapi source", "mode", mode.String(), "control_plane", cfg.FleetBrokerURL,
		"methods", coreAPIRemoteMethods,
		"note", "this worker asks the control plane for the methods the remote transport carries — reads, the claim/outcome writes, and the manual reply/compose protocol — and brokers their credentials separately; it still opens a pool for the rest")
	return coreAPIWiring{mode: mode, client: client}, nil
}

// coreOptions returns the inprocess options this wiring implies. The local mode
// returns NONE, so inprocess.New builds exactly the client it built before this
// slice existed — that emptiness is the self-host guarantee, not an oversight.
func (w coreAPIWiring) coreOptions() []inprocess.Option {
	if w.client == nil {
		return nil
	}
	// All four sources, one client. They are separate seams because they shipped
	// in separate slices, not because a deployment would ever want one without
	// the others — and the write seams in particular MUST arrive with the job
	// seam: a worker fetching its work remotely while claiming it locally would
	// be two processes disagreeing about who owns a send.
	return []inprocess.Option{
		inprocess.WithRemoteSuppression(w.client),
		inprocess.WithRemoteJobs(w.client),
		inprocess.WithRemoteOutcomes(w.client),
		inprocess.WithRemoteInboxSends(w.client),
	}
}
