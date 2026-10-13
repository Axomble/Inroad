package worker

import (
	"testing"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/jobrun"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/worker/deliverability"
	"github.com/inroad/inroad/internal/worker/fleet"
	"github.com/inroad/inroad/internal/worker/inbox"
	"github.com/inroad/inroad/internal/worker/maintenance"
	"github.com/inroad/inroad/internal/worker/recipientesp"
	"github.com/inroad/inroad/internal/worker/testsend"
	webhookworker "github.com/inroad/inroad/internal/worker/webhook"
)

// THE capability census, for BOTH coreapi implementations, with no database.
//
// # What this exists to catch
//
// Nineteen capabilities are reached by COMMA-OK TYPE ASSERTION on whatever
// value the composition root put in Deps.Core — six in handlers.go, three in
// inbox.RegisterPerMessage, six feature-detected per message inside
// PollHandler, one resolved once at PollHandler's wiring (the poll backoff),
// and three in cmd/worker. A type assertion is invisible to the
// compiler. When one stops matching, nothing fails to build: the registrar
// logs (or does not) and skips, the process starts, reports healthy, consumes
// its queues, and silently does less than it did yesterday.
//
// That failure has a shape. `deliverability:evaluate` with no breaker
// registered is a campaign whose bounce rate breaches and which is never
// paused, with the handler reporting success. `inbox:poll` without
// WarmupEvidenceClient logs `inbox_poll_warmup_evidence_unavailable` and
// misclassifies warm-up DSNs as campaign bounces. Neither breaks a build and
// neither fails a unit test that uses a fake, because every fake in this
// repository satisfies the narrow interfaces whether Register wires them or
// not.
//
// # Why it covers both implementations, in one table
//
// Slice 4 gave a `role=send` worker a *remote.Client instead of an inprocess
// client, so there are now TWO dynamic types that have to carry this set, and
// a test that checked one would leave the other free to rot. The in-process
// half is the important one: it is the self-host path (`role=all`), the
// topology every compose file, Helm chart and Terraform config runs, and the
// one thing this whole programme has been forbidden to break.
//
// # Why it needs no database
//
// A type assertion asks the method set, it does not call anything, and
// inprocess.New only STORES its pool — it issues no query at construction. So
// a nil pool is enough to obtain the real dynamic type, and this stays a unit
// test that runs on every `go test ./...` rather than behind the integration
// tag where a capability regression could hide until someone had a database
// up. The integration twins (register_integration_test.go,
// role_registration_test.go) prove the handlers then WORK; this proves they
// are reachable at all.
func TestBothCoreAPIImplementationsCarryEveryOptionalCapability(t *testing.T) {
	remoteClient, err := remote.NewClient(
		"https://control.internal:8090",
		"0123456789abcdef0123456789abcdef", // credbroker.MinTokenLen
		false,
		unreachableOpener{},
	)
	if err != nil {
		t.Fatalf("remote.NewClient: %v", err)
	}

	for _, impl := range []struct {
		name string
		core coreapi.Client
		// controlPlaneOnly marks the capabilities that belong to the SCHEDULED
		// half. *remote.Client deliberately does not implement them: a fleet
		// host runs no cross-tenant sweep, and refusing to implement the
		// interface is how registerScheduled is told so. Asserting they are
		// ABSENT is as load-bearing as asserting the rest are present — a
		// remote client that satisfied maintenance.Cleaner would register a
		// purge on a host that must never run one.
		controlPlaneOnly bool
	}{
		{
			name: "in-process (role=all and role=control — the self-host path)",
			// A nil pool is legal here and is never dereferenced: New stores it
			// and this test only asks the method set. See the doc above.
			core: inprocess.New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil),
		},
		{
			name:             "remote (role=send — the fleet path)",
			core:             remoteClient,
			controlPlaneOnly: true,
		},
	} {
		t.Run(impl.name, func(t *testing.T) {
			// The PER-MESSAGE half. Both implementations must carry all of it:
			// a fleet host runs exactly these handlers, and a role=all host
			// runs them too.
			for _, tc := range []struct {
				capability string
				consumer   string
				cost       string
				ok         bool
			}{
				{"testsend.Core", "handlers.go registration", "testsend:send is never registered; the preview silently never sends",
					assertsAs[testsend.Core](impl.core)},
				{"webhookworker.Core", "handlers.go registration", "webhook:deliver is never registered; no outbound webhook fires",
					assertsAs[webhookworker.Core](impl.core)},
				{"inbox.ReplyCore", "inbox.RegisterPerMessage", "the legacy inbox:reply_send drain never runs; in-flight replies are lost at cutover",
					assertsAs[inbox.ReplyCore](impl.core)},
				{"inbox.PendingReplyCore", "inbox.RegisterPerMessage", "inbox:pending_reply_send is never registered; a queued reply sits scheduled forever",
					assertsAs[inbox.PendingReplyCore](impl.core)},
				{"inbox.ComposeCore", "inbox.RegisterPerMessage", "inbox:pending_compose_send is never registered; a composed email never leaves",
					assertsAs[inbox.ComposeCore](impl.core)},
				{"coreapi.CRMCaptureClient", "inbox/dispatch.go, per message", "a positive reply is never captured as a CRM activity",
					assertsAs[coreapi.CRMCaptureClient](impl.core)},
				{"coreapi.ReplyLabelClient", "inbox/dispatch.go, per message", "label automation falls back to the pre-taxonomy class switch",
					assertsAs[coreapi.ReplyLabelClient](impl.core)},
				{"coreapi.InboxCaptureClient", "inbox/poll.go, per message", "a matched reply is never stored; the unified inbox stays empty",
					assertsAs[coreapi.InboxCaptureClient](impl.core)},
				{"coreapi.WarmupEvidenceClient", "inbox/poll.go, per message", "warm-up DSNs are misclassified as campaign bounces (inbox_poll_warmup_evidence_unavailable)",
					assertsAs[coreapi.WarmupEvidenceClient](impl.core)},
				{"coreapi.WarmupSendLookupClient", "inbox/poll.go, per message", "an M365 warm-up message whose token the provider stripped is never recovered",
					assertsAs[coreapi.WarmupSendLookupClient](impl.core)},
				{"coreapi.DeliverabilityComplaintClient", "inbox/poll.go, per message", "a mail-borne abuse report is never ingested and never suppresses",
					assertsAs[coreapi.DeliverabilityComplaintClient](impl.core)},
				{"inbox.PollBackoffCore", "inbox/poll.go, resolved once at wiring", "an unreachable mail server is re-dialed every sweep interval forever (inbox_poll_backoff_unavailable)",
					assertsAs[inbox.PollBackoffCore](impl.core)},
				{"coreapi.DeadLetterClient", "cmd/worker/main.go", "an exhausted task vanishes instead of reaching task_dead_letters",
					assertsAs[coreapi.DeadLetterClient](impl.core)},
				{"coreapi.ProviderSignalClient", "cmd/worker/main.go", "per-worker provider verdicts are collected and never reported",
					assertsAs[coreapi.ProviderSignalClient](impl.core)},
			} {
				if !tc.ok {
					t.Errorf("%s does not satisfy %s (asserted in %s) — %s",
						impl.name, tc.capability, tc.consumer, tc.cost)
				}
			}

			// The SCHEDULED half. Present in process, deliberately absent
			// remotely.
			for _, tc := range []struct {
				capability string
				cost       string
				ok         bool
			}{
				{"deliverability.Breaker", "a breaching campaign is evaluated and never paused, and the handler reports success",
					assertsAs[deliverability.Breaker](impl.core)},
				{"maintenance.Cleaner", "maintenance:cleanup is never registered; nothing is ever purged",
					assertsAs[maintenance.Cleaner](impl.core)},
				{"maintenance.AuditPurger", "INROAD_AUDIT_RETENTION_DAYS is silently ignored; audit events are never purged",
					assertsAs[maintenance.AuditPurger](impl.core)},
				{"recipientesp.Core", "the recipient-ESP cache is never refreshed; sender matching degrades to unmatched",
					assertsAs[recipientesp.Core](impl.core)},
				{"fleet.Rotator", "a mailbox on a provider-blocked worker is never moved off it",
					assertsAs[fleet.Rotator](impl.core)},
				{"jobrun.Recorder", "no periodic reconcile is recorded in the scheduled-job ledger",
					assertsAs[jobrun.Recorder](impl.core)},
			} {
				switch {
				case impl.controlPlaneOnly && tc.ok:
					t.Errorf("%s satisfies %s — a fleet host must run no cross-tenant sweep, and the comma-ok is what stops it",
						impl.name, tc.capability)
				case !impl.controlPlaneOnly && !tc.ok:
					t.Errorf("%s does not satisfy %s — %s", impl.name, tc.capability, tc.cost)
				}
			}
		})
	}
}
