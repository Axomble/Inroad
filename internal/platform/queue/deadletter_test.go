package queue

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// asynq populates the retry counters through its internal context package,
// which this module cannot import — so a synthetic asynq-processed context
// cannot be built here. The handler is therefore exercised in two halves:
//
//   - isLastAttempt, the exhaustion DECISION (the part that could actually be
//     wrong), is tested directly at its boundary — which is why it is split out
//     of isTerminalFailure at all.
//   - The surrounding capture behaviour (nil guards, the no-counters
//     conservative default, payload ownership) is tested through the handler.
//
// The context plumbing between them — the two asynq accessors — is asynq's own
// contract and is covered by the integration path, not re-asserted here.
type fakeRecorder struct {
	calls []DeadLetter
	err   error
}

func (f *fakeRecorder) RecordDeadLetter(_ context.Context, in DeadLetter) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, in)
	return nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A context with no asynq counters must NOT be treated as terminal: capturing
// then would write a dead letter for a task that is still going to be retried.
// This is the conservative default the doc comment promises.
func TestIsTerminalFailureIsFalseWithoutCounters(t *testing.T) {
	_, _, terminal := isTerminalFailure(context.Background(), errors.New("boom"))
	if terminal {
		t.Error("a context with no retry counters was treated as terminal")
	}
}

// The handler must no-op on a context with no counters, for the same reason.
func TestHandlerIgnoresAFailureWithNoCounters(t *testing.T) {
	rec := &fakeRecorder{}
	h := DeadLetterErrorHandler(rec, quietLogger())
	ws := uuid.New().String()
	task := asynq.NewTask(TaskSequenceAdvance, mustPayload(t, ws))

	h.HandleError(context.Background(), task, errors.New("dial timeout"))

	if len(rec.calls) != 0 {
		t.Fatalf("recorded %d dead letters for a non-terminal failure, want 0", len(rec.calls))
	}
}

// A nil recorder (capture not wired) must be a silent no-op rather than a panic
// in the worker's failure path.
func TestHandlerWithNoRecorderDoesNotPanic(t *testing.T) {
	h := DeadLetterErrorHandler(nil, quietLogger())
	h.HandleError(context.Background(), asynq.NewTask(TaskInboxPoll, nil), errors.New("boom"))
}

// A nil task must not panic either — asynq's interface does not forbid it.
func TestHandlerWithNilTaskDoesNotPanic(t *testing.T) {
	rec := &fakeRecorder{}
	h := DeadLetterErrorHandler(rec, quietLogger())
	h.HandleError(context.Background(), nil, errors.New("boom"))
	if len(rec.calls) != 0 {
		t.Errorf("recorded %d dead letters for a nil task, want 0", len(rec.calls))
	}
}

// Even SkipRetry — unambiguously terminal on its own — must not capture when
// the counters are absent, because their absence means this was not a real
// processed task. The conservative default applies to every error kind.
func TestIsTerminalFailureNeedsCountersEvenForSkipRetry(t *testing.T) {
	for _, err := range []error{asynq.SkipRetry, asynq.RevokeTask, errWrap(asynq.SkipRetry)} {
		if _, _, terminal := isTerminalFailure(context.Background(), err); terminal {
			t.Errorf("%v: terminal without counters; want the conservative false", err)
		}
	}
}

func errWrap(err error) error { return errors.Join(errors.New("handler failed"), err) }

// The exhaustion decision itself, at the boundary where an off-by-one would
// live: too strict and a genuinely lost send is never captured, too loose and a
// task about to be retried is recorded as dropped.
//
// sendMaxRetry (5) and pollMaxRetry (2) are used as the real-world rows so a
// change to either constant that broke the arithmetic shows up here.
func TestIsLastAttempt(t *testing.T) {
	boom := errors.New("dial timeout")
	cases := []struct {
		name     string
		retried  int
		maxRetry int
		err      error
		want     bool
	}{
		{"send: first of five attempts", 0, sendMaxRetry, boom, false},
		{"send: one short of exhaustion", sendMaxRetry - 1, sendMaxRetry, boom, false},
		{"send: exactly exhausted", sendMaxRetry, sendMaxRetry, boom, true},
		{"send: past exhaustion", sendMaxRetry + 1, sendMaxRetry, boom, true},
		{"poll: one short of exhaustion", pollMaxRetry - 1, pollMaxRetry, boom, false},
		{"poll: exhausted", pollMaxRetry, pollMaxRetry, boom, true},
		{"no retries configured: the first failure is terminal", 0, 0, boom, true},
		{"SkipRetry is terminal on the first attempt", 0, sendMaxRetry, asynq.SkipRetry, true},
		{"RevokeTask is terminal on the first attempt", 0, sendMaxRetry, asynq.RevokeTask, true},
		{"wrapped SkipRetry is still terminal", 0, sendMaxRetry, errWrap(asynq.SkipRetry), true},
		{"a nil error at exhaustion is still terminal", sendMaxRetry, sendMaxRetry, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLastAttempt(tc.retried, tc.maxRetry, tc.err); got != tc.want {
				t.Errorf("isLastAttempt(retried=%d, maxRetry=%d, %v) = %v, want %v",
					tc.retried, tc.maxRetry, tc.err, got, tc.want)
			}
		})
	}
}

// workspaceFromPayload is what decides whether a dead letter can be OWNED, so
// its edges matter: every real task payload must resolve, and the payload-less
// sweep tasks must not.
func TestWorkspaceFromPayload(t *testing.T) {
	ws := uuid.New().String()

	t.Run("resolves every real task payload shape", func(t *testing.T) {
		payloads := map[string]any{
			"AdvancePayload":                 AdvancePayload{EnrollmentID: uuid.New().String(), WorkspaceID: ws},
			"InboxPollPayload":               InboxPollPayload{MailboxID: uuid.New().String(), WorkspaceID: ws},
			"WarmupTickPayload":              WarmupTickPayload{MailboxID: uuid.New().String(), WorkspaceID: ws},
			"WarmupEngagePayload":            WarmupEngagePayload{ReceiptID: uuid.New().String(), WorkspaceID: ws},
			"TestSendPayload":                TestSendPayload{CampaignID: uuid.New().String(), WorkspaceID: ws},
			"InboxReplySendPayload":          InboxReplySendPayload{ThreadID: uuid.New().String(), WorkspaceID: ws},
			"InboxPendingReplySendPayload":   InboxPendingReplySendPayload{PendingID: uuid.New().String(), WorkspaceID: ws},
			"InboxPendingComposeSendPayload": InboxPendingComposeSendPayload{PendingID: uuid.New().String(), WorkspaceID: ws},
			"DeliverabilityEvaluatePayload":  DeliverabilityEvaluatePayload{CampaignID: uuid.New().String(), WorkspaceID: ws},
		}
		for name, p := range payloads {
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("marshal %s: %v", name, err)
			}
			got, ok := workspaceFromPayload(b)
			if !ok {
				t.Errorf("%s: no workspace resolved from %s", name, b)
				continue
			}
			if got != ws {
				t.Errorf("%s: workspace = %q, want %q", name, got, ws)
			}
		}
	})

	t.Run("rejects what cannot be owned", func(t *testing.T) {
		cases := []struct {
			name    string
			payload []byte
		}{
			{"nil payload (inbox:sweep, warmup:sweep, maintenance:cleanup)", nil},
			{"empty payload", []byte{}},
			{"not JSON", []byte("{oops")},
			{"JSON but not an object", []byte(`[1,2,3]`)},
			{"object with no workspace_id", []byte(`{"mailbox_id":"x"}`)},
			{"empty workspace_id", []byte(`{"workspace_id":""}`)},
		}
		for _, tc := range cases {
			if _, ok := workspaceFromPayload(tc.payload); ok {
				t.Errorf("%s: resolved a workspace it should not have", tc.name)
			}
		}
	})
}

// errorMessage must survive a nil error rather than panicking inside the
// failure path.
func TestErrorMessage(t *testing.T) {
	if got := errorMessage(nil); got != "" {
		t.Errorf("errorMessage(nil) = %q, want empty", got)
	}
	if got := errorMessage(errors.New("dial timeout")); got != "dial timeout" {
		t.Errorf("errorMessage = %q, want %q", got, "dial timeout")
	}
}

func mustPayload(t *testing.T, ws string) []byte {
	t.Helper()
	b, err := json.Marshal(AdvancePayload{EnrollmentID: uuid.New().String(), WorkspaceID: ws})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
