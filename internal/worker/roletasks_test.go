package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/queue"
)

// scheduledTasks are the periodic reconciles plus the breaker: control-role
// only. Keep in sync with cmd/worker/scheduler.go sweepRegistrars().
var scheduledTasks = []string{
	queue.TaskMaintenanceCleanup,
	queue.TaskDomainAuthSweep,
	queue.TaskRecipientESPSweep,
	queue.TaskFleetRotate,
	queue.TaskWarmupSweep,
	queue.TaskSweepEnrollments,
	queue.TaskInboxSweep,
	queue.TaskInboxPendingSendSweep,
	queue.TaskDeliverabilityEvaluate,
}

// perMessageTasks are the handlers a fleet host runs. Together with
// scheduledTasks above this is EVERY Task* constant in platform/queue: an
// unlisted constant is a handler whose role nothing asserts, which is how the
// drain-only inbox:reply_send below went uncovered.
var perMessageTasks = []string{
	queue.TaskWarmupTick,
	queue.TaskWarmupEngage,
	queue.TaskTestSend,
	queue.TaskSequenceAdvance,
	queue.TaskInboxPoll,
	// Deprecated and drain-only — nothing enqueues it any more — but
	// inbox.RegisterPerMessage still registers it behind ReplyCore so tasks
	// already in Redis at cutover get delivered. That makes it per-message work
	// like any other: a control host must not claim it, and a send host must
	// still drain it. Delete this line with the constant, its handler and
	// ReplyCore, not before.
	//nolint:staticcheck // SA1019: asserting the DRAIN registration's role is the point.
	queue.TaskInboxReplySend,
	queue.TaskInboxPendingReplySend,
	queue.TaskInboxPendingComposeSend,
	queue.TaskWebhookDeliver,
}

// registered reports whether mux routes taskType. asynq's ServeMux answers that
// without running anything: Handler returns the matched pattern, or an empty
// one alongside its NotFoundHandler for a type nothing claims. That is the real
// routing table, resolved by asynq's own longest-prefix rules — the same lookup
// ProcessTask does one line before it invokes.
//
// It asks instead of dispatching, which is the one place these tests depart
// from TestRegisterWiresTheDeliverabilityBreaker. There, dispatching is the
// point: one task, fixtures seeded for it, an effect asserted afterwards. Here
// the mux carries every handler at once over the shared integration database
// and the assertion is only "is it routed", so dispatching all fifteen buys
// nothing and costs a great deal. Measured, not assumed: dispatching
// maintenance:cleanup purged 72 idempotency keys, 20 dead letters and 3 worker
// assignments belonging to other packages' tests, which run concurrently under
// `go test -p 4`, and domainauth:sweep then panicked in dnsauth.lookup because
// the nil Resolver those Deps carry meets whatever sending domains the rest of
// the suite has left in the database.
//
// This file carries NO build tag on purpose. The lists and this helper are
// shared with role_registration_test.go (integration, needs a pool for the
// in-process client) and with the poolless test below, which needs nothing at
// all — and two copies of "which tasks belong to which role" is how one of them
// stops being true.
func registered(t *testing.T, mux *asynq.ServeMux, taskType string) bool {
	t.Helper()
	_, pattern := mux.Handler(asynq.NewTask(taskType, nil))
	return pattern != ""
}

// unreachableOpener stands in for the credential broker remote.NewClient
// requires. Nothing here opens a credential — the question is which handlers
// REGISTER — so every call is an error rather than a plausible secret.
type unreachableOpener struct{}

func (unreachableOpener) OpenMailbox(context.Context, credbroker.MailboxRef) (credbroker.MailboxSecret, error) {
	return credbroker.MailboxSecret{}, errors.New("no broker in this test")
}

func (unreachableOpener) OpenWebhookEndpointSecret(context.Context, uuid.UUID, uuid.UUID, []byte) ([]byte, error) {
	return nil, errors.New("no broker in this test")
}

// THE structural test for slice 4: a role=send worker wired over the remote
// coreapi transport registers every per-message handler and no scheduled one —
// with NO DATABASE ANYWHERE.
//
// Note what this test does not have: a pool, a DSN, a migration, an
// integration build tag. It cannot have one, because *remote.Client has no
// database access to give it. That absence is the point. Its integration twin
// (role_registration_test.go) asks the same question of the in-process client
// and NEEDS a pool to ask it; this one proves a fleet host can answer it with
// nothing but a URL and a token.
//
// Why it is worth a test rather than a comment. FIVE of these handlers are
// registered behind COMMA-OK TYPE ASSERTIONS on the coreapi client — testsend.Core
// and webhookworker.Core in handlers.go, and ReplyCore, PendingReplyCore and
// ComposeCore in inbox.RegisterPerMessage — and a type assertion is invisible
// to the compiler. A method whose signature drifted, or one this slice forgot
// to carry, would not break the build; it would make a fleet worker start,
// report healthy, consume its queues, and silently never send a test send,
// never deliver a webhook, and never deliver a deferred reply, while every
// test that uses the in-process client stayed green.
//
// The SIX optional inbox capabilities are a different shape and are not covered
// here, which is worth being exact about: CRMCaptureClient, InboxCaptureClient,
// WarmupEvidenceClient, DeliverabilityComplaintClient, WarmupSendLookupClient
// and ReplyLabelClient are asserted INSIDE PollHandler, per message, so
// inbox:poll registers whether or not the client satisfies them. What pins
// those is the compile-time `var _` block at the bottom of
// internal/coreapi/remote/inbound.go — a build failure rather than a silent
// degrade — and TestASendWorkerRecognisesEveryOptionalInboxCapability below,
// which makes the same six assertions from the CONSUMER's side.
//
// The negative half matters just as much: a cross-tenant sweep leaking onto
// this role would hand a host we do not control the ability to enumerate every
// workspace's due enrollments, and *remote.Client answers those with
// ErrControlPlaneOnly rather than data — but not registering them at all is the
// barrier that should never have to be tested.
func TestASendWorkerOverTheRemoteTransportRegistersEveryPerMessageHandler(t *testing.T) {
	core, err := remote.NewClient(
		"https://control.internal:8090",
		"0123456789abcdef0123456789abcdef", // credbroker.MinTokenLen
		false,                              // https, no plaintext opt-out
		unreachableOpener{},
	)
	if err != nil {
		t.Fatalf("remote.NewClient: %v", err)
	}

	mux := asynq.NewServeMux()
	Register(mux, Deps{Role: RoleSend, Core: core, PublicURL: "https://app.test"})

	for _, tt := range perMessageTasks {
		if !registered(t, mux, tt) {
			t.Errorf("a poolless send worker did not register %q — a capability assertion against *remote.Client failed, "+
				"so a fleet host would start healthy and silently never do this work", tt)
		}
	}
	for _, tt := range scheduledTasks {
		if registered(t, mux, tt) {
			t.Errorf("a poolless send worker registered %q — a fleet host must not run cross-tenant scans or purges", tt)
		}
	}
}

// The six optional inbox capabilities, asserted from the CONSUMER's side — the
// same comma-ok the poller performs, against the same value a fleet host holds.
//
// These do not gate registration: inbox:poll is registered unconditionally and
// each capability is checked per message, inside PollHandler. So the failure
// they guard is not "a handler is missing", it is "a poll runs, advances its
// cursor, and quietly throws away everything it learned" — no inbound message
// stored, no CRM reply captured, no complaint ingested, no warmup receipt
// recorded, no reply label resolved. A fleet worker in that state looks
// perfectly healthy and loses a tenant's replies.
//
// internal/coreapi/remote/inbound.go pins the same six at BUILD time, which is
// strictly stronger. This is here because the assertion that matters is the one
// the CONSUMER makes, and it is this package's job to notice if the two ever
// disagree.
func TestASendWorkerRecognisesEveryOptionalInboxCapability(t *testing.T) {
	c, err := remote.NewClient(
		"https://control.internal:8090",
		"0123456789abcdef0123456789abcdef",
		false,
		unreachableOpener{},
	)
	if err != nil {
		t.Fatalf("remote.NewClient: %v", err)
	}
	// Held as coreapi.Client, exactly as internal/worker/inbox holds it, so the
	// assertion runs against the same interface value the poller asserts on.
	var core coreapi.Client = c

	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"CRM capture", assertsAs[coreapi.CRMCaptureClient](core)},
		{"inbound message storage — a tenant's replies would vanish", assertsAs[coreapi.InboxCaptureClient](core)},
		{"warmup evidence", assertsAs[coreapi.WarmupEvidenceClient](core)},
		{"mail-borne complaint ingest", assertsAs[coreapi.DeliverabilityComplaintClient](core)},
		{"warmup send recovery for a stripped token", assertsAs[coreapi.WarmupSendLookupClient](core)},
		{"reply label resolution", assertsAs[coreapi.ReplyLabelClient](core)},
		// And the two cmd/worker feature-detects on the client it built.
		{"dead-letter capture", assertsAs[coreapi.DeadLetterClient](core)},
		{"provider signal reporting", assertsAs[coreapi.ProviderSignalClient](core)},
	} {
		if !tc.ok {
			t.Errorf("a poolless send worker does not satisfy %s", tc.name)
		}
	}
}

// assertsAs is the comma-ok a consumer performs, as an expression. It exists so
// the table above reads as a list of capabilities rather than eight copies of
// the same three lines.
func assertsAs[T any](v any) bool {
	_, ok := v.(T)
	return ok
}
