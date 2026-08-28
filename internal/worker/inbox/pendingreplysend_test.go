package inbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

// stubPendingReplyCore is the mocked PendingReplyCore: no Keyring, no provider,
// no Postgres. It records the reasons it was asked to persist, because those —
// not the errors the handler returns to asynq — are what a client reads back out
// of last_error.
type stubPendingReplyCore struct {
	pending    coreapi.PendingInboxReply
	claimErr   error
	transport  coreapi.SenderTransport
	transportE error
	suppressed bool
	suppressE  error

	releaseReasons []string
	failReasons    []string
}

func (s *stubPendingReplyCore) ClaimPendingInboxReply(context.Context, string, string) (coreapi.PendingInboxReply, error) {
	return s.pending, s.claimErr
}

func (s *stubPendingReplyCore) MarkPendingInboxReplySent(context.Context, string, string, string) error {
	return nil
}

func (s *stubPendingReplyCore) ReleasePendingInboxReply(_ context.Context, _, _, reason string) error {
	s.releaseReasons = append(s.releaseReasons, reason)
	return nil
}

func (s *stubPendingReplyCore) FailPendingInboxReply(_ context.Context, _, _, reason string) error {
	s.failReasons = append(s.failReasons, reason)
	return nil
}

func (s *stubPendingReplyCore) IsSuppressed(context.Context, string, string) (bool, error) {
	return s.suppressed, s.suppressE
}

func (s *stubPendingReplyCore) ResolveSenderTransport(context.Context, string, string) (coreapi.SenderTransport, error) {
	return s.transport, s.transportE
}

func (s *stubPendingReplyCore) RecordInboxReply(context.Context, coreapi.RecordInboxReplyInput) error {
	return nil
}

func pendingReplyTask(t *testing.T) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.InboxPendingReplySendPayload{PendingID: "pending-1", WorkspaceID: "ws-1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return asynq.NewTask(queue.TaskInboxPendingReplySend, b)
}

func readyPendingReplyCore() *stubPendingReplyCore {
	return &stubPendingReplyCore{
		pending: coreapi.PendingInboxReply{
			ThreadID: "thread-1", BodyText: "here is our pricing",
			Job: coreapi.InboxReplyJob{MailboxID: "mbox-1", Subject: "Pricing?", ToEmail: "lead@x.test"},
		},
		transport: coreapi.SenderTransport{Provider: "smtp", FromEmail: "me@x.test"},
	}
}

// providerNoise is a realistic go-mail SendError string. Its Error() concatenates
// the server's RAW response text AND ", affected recipient(s): <address>", so a
// wrapped provider error carries both the remote host's message and a contact's
// email address.
const providerNoise = "smtp: 550 5.7.1 SENTINEL-relay-refused-for-2f81, affected recipient(s): lead@x.test"

// THE ERROR A FAILED ATTEMPT RETURNS IS A PUBLISHED STRING.
//
// asynq hands the last attempt's error to DeadLetterErrorHandler, which stores
// errorMessage(taskErr) in task_dead_letters.last_error — and GET /dead-letters
// serves last_error to any campaigns:read caller, an OAuth-grantable scope. So
// returning the wrapped provider error published the remote server's raw
// rejection text and the recipient's address, on precisely the surface this
// change exists to keep free of them. The handler's own doc claimed the raw
// error was kept out of what clients see; it was true of last_error on the row
// and false of the dead letter.
//
// The fix is the rule the row already followed: a STABLE TOKEN out, the cause to
// the log.
func TestPendingReplySendReturnsAStableReasonNeverTheProviderError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(*stubPendingReplyCore) *stubMailer
		reason  string
	}{
		{
			name: "the provider rejected the message",
			arrange: func(*stubPendingReplyCore) *stubMailer {
				return &stubMailer{err: errors.New(providerNoise)}
			},
			reason: reasonProviderRejected,
		},
		{
			name: "the transport could not be resolved",
			arrange: func(c *stubPendingReplyCore) *stubMailer {
				c.transportE = errors.New(providerNoise)
				return &stubMailer{}
			},
			reason: reasonTransportUnavailable,
		},
		{
			name: "the suppression check failed",
			arrange: func(c *stubPendingReplyCore) *stubMailer {
				c.suppressE = errors.New(providerNoise)
				return &stubMailer{}
			},
			reason: reasonSuppressionCheckFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := slog.Default()
			var logs bytes.Buffer
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(restore) })

			core := readyPendingReplyCore()
			mailer := tc.arrange(core)

			err := PendingReplySendHandler(core, mailer)(context.Background(), pendingReplyTask(t))
			if err == nil {
				t.Fatal("err = nil, want a retryable failure")
			}
			if strings.Contains(err.Error(), "SENTINEL-relay-refused-for-2f81") {
				t.Errorf("the returned error carries the provider's raw response, which is stored "+
					"in task_dead_letters.last_error and served under campaigns:read: %v", err)
			}
			if strings.Contains(err.Error(), "lead@x.test") {
				t.Errorf("the returned error names the recipient: %v", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("err = %v, want it to carry the stable reason %q — an operator still needs "+
					"to know WHY", err, tc.reason)
			}
			// The row keeps saying the same thing it always did.
			if len(core.releaseReasons) != 1 || core.releaseReasons[0] != tc.reason {
				t.Errorf("release reasons = %v, want exactly [%q]", core.releaseReasons, tc.reason)
			}
			// And the cause is not LOST — it goes where operators can read it and
			// clients cannot.
			if !strings.Contains(logs.String(), "SENTINEL-relay-refused-for-2f81") {
				t.Errorf("the underlying cause reached neither the client nor the log, so nobody "+
					"can diagnose the failure: %s", logs.String())
			}
		})
	}
}

// The happy path still works, and the body still comes from the ROW rather than
// the task — without this the test above could pass on a handler that never
// sends at all.
func TestPendingReplySendHandlerSendsTheRowsBody(t *testing.T) {
	core := readyPendingReplyCore()
	mailer := &stubMailer{}

	if err := PendingReplySendHandler(core, mailer)(context.Background(), pendingReplyTask(t)); err != nil {
		t.Fatalf("PendingReplySendHandler: %v", err)
	}
	if mailer.calls != 1 {
		t.Fatalf("mailer calls = %d, want 1", mailer.calls)
	}
	if mailer.msg.BodyText != "here is our pricing" || mailer.msg.To != "lead@x.test" {
		t.Errorf("msg = %+v, want the row's body and the job's recipient", mailer.msg)
	}
	if mailer.msg.Subject != "Re: Pricing?" {
		t.Errorf("subject = %q, want an idempotent Re: prefix", mailer.msg.Subject)
	}
}

// The undo path: an unclaimable row is done, not failed. A returned error here
// would retry a reply the operator explicitly cancelled.
func TestPendingReplySendHandlerTreatsAnUnclaimableRowAsDone(t *testing.T) {
	core := readyPendingReplyCore()
	core.claimErr = coreapi.ErrInboxPendingNotClaimable
	mailer := &stubMailer{}

	if err := PendingReplySendHandler(core, mailer)(context.Background(), pendingReplyTask(t)); err != nil {
		t.Fatalf("err = %v, want nil — a cancelled reply is not a failure", err)
	}
	if mailer.calls != 0 {
		t.Error("an unclaimable row must not dial the provider")
	}
}
