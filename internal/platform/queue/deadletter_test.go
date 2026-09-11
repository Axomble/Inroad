package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

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
		// The shared list (payload_content_test.go), so a payload added there and
		// forgotten here — or the reverse — cannot exist.
		for name, p := range allTaskPayloads(ws) {
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

// THE SUPPRESSION. A terminal inbox:reply_send must record NOTHING, because its
// payload carries the operator's reply body and a captured row is served by
// GET /dead-letters under campaigns:read — an OAuth-grantable scope, unlike
// inbox:read. Every other task type must still record: this is a targeted,
// temporary exception, not a hole in capture.
//
// Driven through recordTerminalFailure rather than the exported handler because
// asynq populates its retry counters through an internal package this module
// cannot import, so a genuinely terminal context cannot be built here — the same
// reason isLastAttempt is split out and tested at its own boundary.
func TestTerminalFailureOfTheLegacyReplySendIsNotCaptured(t *testing.T) {
	ws := uuid.New().String()
	const sentinel = "the-operators-private-reply-text"

	t.Run("inbox:reply_send records nothing and logs no payload", func(t *testing.T) {
		rec := &fakeRecorder{}
		var logged bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logged, nil))
		payload, err := json.Marshal(InboxReplySendPayload{
			ThreadID: uuid.New().String(), BodyText: sentinel, WorkspaceID: ws, TaskID: "task-1",
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		recordTerminalFailure(context.Background(), rec, logger,
			asynq.NewTask(TaskInboxReplySend, payload), errors.New("smtp: connection refused"), QueueSend, 6, sendMaxRetry)

		if len(rec.calls) != 0 {
			t.Fatalf("recorded %d dead letters for inbox:reply_send, want 0 — the payload is "+
				"correspondence and the row would be readable under campaigns:read", len(rec.calls))
		}
		if strings.Contains(logged.String(), sentinel) {
			t.Errorf("the reply body was written to the log: %s", logged.String())
		}
		// Suppressing capture must still be LOUD: this is a permanently dropped
		// send, and the operator's only remaining signal is this line.
		if !strings.Contains(logged.String(), TaskInboxReplySend) {
			t.Errorf("the suppressed failure was not logged at all: %s", logged.String())
		}
	})

	t.Run("every other task type still records", func(t *testing.T) {
		payloads := map[string]any{
			TaskSequenceAdvance:         AdvancePayload{EnrollmentID: uuid.New().String(), WorkspaceID: ws},
			TaskInboxPoll:               InboxPollPayload{MailboxID: uuid.New().String(), WorkspaceID: ws},
			TaskWarmupTick:              WarmupTickPayload{MailboxID: uuid.New().String(), WorkspaceID: ws},
			TaskWarmupEngage:            WarmupEngagePayload{ReceiptID: uuid.New().String(), WorkspaceID: ws},
			TaskTestSend:                TestSendPayload{CampaignID: uuid.New().String(), WorkspaceID: ws},
			TaskInboxPendingReplySend:   InboxPendingReplySendPayload{PendingID: uuid.New().String(), WorkspaceID: ws},
			TaskInboxPendingComposeSend: InboxPendingComposeSendPayload{PendingID: uuid.New().String(), WorkspaceID: ws},
			TaskDeliverabilityEvaluate:  DeliverabilityEvaluatePayload{CampaignID: uuid.New().String(), WorkspaceID: ws},
		}
		for taskType, p := range payloads {
			rec := &fakeRecorder{}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatalf("marshal %s: %v", taskType, err)
			}
			recordTerminalFailure(context.Background(), rec, quietLogger(),
				asynq.NewTask(taskType, b), errors.New("dial timeout"), QueueSend, 6, sendMaxRetry)
			if len(rec.calls) != 1 {
				t.Errorf("%s: recorded %d dead letters, want 1 — the suppression must be "+
					"targeted, not a hole in capture", taskType, len(rec.calls))
				continue
			}
			if rec.calls[0].TaskType != taskType || rec.calls[0].WorkspaceID != ws {
				t.Errorf("%s: recorded %+v, want the task type and its workspace", taskType, rec.calls[0])
			}
		}
	})

	// One entry, and it names the drain type. A second entry means someone is
	// suppressing capture for a task that has no drain plan behind it.
	t.Run("exactly one task type is suppressed", func(t *testing.T) {
		if len(legacyContentBearingTaskTypes) != 1 {
			t.Fatalf("legacyContentBearingTaskTypes = %v, want exactly the one drain type", legacyContentBearingTaskTypes)
		}
		if !IsLegacyContentBearingTaskType(TaskInboxReplySend) {
			t.Errorf("inbox:reply_send is not suppressed")
		}
		if IsLegacyContentBearingTaskType(TaskInboxPendingReplySend) {
			t.Errorf("inbox:pending_reply_send is suppressed; it carries only a row id and must be captured")
		}
	})
}

// The suppression predicate must match EVERYTHING asynq would route to the
// drain handler, not merely the exact registered type.
//
// asynq's ServeMux falls back to strings.HasPrefix over its registered patterns
// (servemux.go match(): exact map lookup, then longest prefix), so a task typed
// `inbox:reply_send<anything>` runs ReplySendHandler and can therefore carry a
// body-bearing InboxReplySendPayload. An exact-match suppression misses exactly
// that type, and the two predicates disagreeing is the bug — the capture gate
// must cover every type the handler it is protecting can be reached by.
//
// Nothing enqueues an arbitrary type today (EnqueueReplay is the only path, and
// it is fed from captured rows), so this is not reachable now. It is the
// disagreement itself that is worth closing: the next arbitrary-type enqueue
// would reopen the disclosure silently.
func TestSuppressionCoversEveryTypeTheDrainHandlerWouldRun(t *testing.T) {
	mux := asynq.NewServeMux()
	// The registration internal/worker/inbox.Register makes for the drain. The
	// deprecated type IS the subject here, so naming it is the point.
	mux.HandleFunc(TaskInboxReplySend, func(context.Context, *asynq.Task) error { return nil })
	mux.HandleFunc(TaskInboxPendingReplySend, func(context.Context, *asynq.Task) error { return nil })

	for _, taskType := range []string{
		TaskInboxReplySend,
		TaskInboxReplySend + ":retry",
		TaskInboxReplySend + "X",
		TaskInboxPendingReplySend,
		TaskInboxPoll,
		"inbox:reply_sen", // one character short: a different type entirely
	} {
		_, pattern := mux.Handler(asynq.NewTask(taskType, nil))
		routesToTheDrain := pattern == TaskInboxReplySend
		if got := IsLegacyContentBearingTaskType(taskType); got != routesToTheDrain {
			t.Errorf("%q: asynq routes it to %q (drain=%v) but IsLegacyContentBearingTaskType = %v — "+
				"the capture gate and the handler that produces the row must agree, or a task "+
				"the drain handler runs is captured with the operator's reply in it",
				taskType, pattern, routesToTheDrain, got)
		}
	}
}

// last_error is BOUNDED, and it has to be for two separate reasons.
//
// The row's own last_error is truncated at 500 (inbox.truncateError); the
// dead-letter copy of the same failure was not truncated at all, so a provider
// that answers with a wall of text wrote all of it into a table that is served
// verbatim under campaigns:read and swept only at 90 days.
//
// The rune boundary is not cosmetic. last_error is a Postgres TEXT column, and
// Postgres REJECTS a byte sequence that is not valid UTF-8 — a naive byte slice
// through a multi-byte rune would fail the INSERT and lose the whole dead
// letter, at exactly the moment the record matters most.
func TestCapturedLastErrorIsBoundedAndStaysValidUTF8(t *testing.T) {
	ws := uuid.New().String()

	capture := func(t *testing.T, taskErr error) DeadLetter {
		t.Helper()
		rec := &fakeRecorder{}
		recordTerminalFailure(context.Background(), rec, quietLogger(),
			asynq.NewTask(TaskSequenceAdvance, mustPayload(t, ws)), taskErr, QueueSend, 6, sendMaxRetry)
		if len(rec.calls) != 1 {
			t.Fatalf("recorded %d dead letters, want 1", len(rec.calls))
		}
		return rec.calls[0]
	}

	t.Run("an ordinary message is stored whole", func(t *testing.T) {
		got := capture(t, errors.New("dial timeout")).LastError
		if got != "dial timeout" {
			t.Errorf("last_error = %q, want the message untouched", got)
		}
	})

	t.Run("a wall of provider text is cut to the same bound the row uses", func(t *testing.T) {
		long := "550 5.7.1 " + strings.Repeat("x", 10_000)
		got := capture(t, errors.New(long)).LastError
		if len(got) > maxLastErrorLength {
			t.Errorf("last_error is %d bytes, want at most %d", len(got), maxLastErrorLength)
		}
		if !strings.HasPrefix(long, got) || got == "" {
			t.Errorf("last_error = %q, want the leading bytes of the real message — the head is "+
				"the diagnostic part", got)
		}
	})

	t.Run("a cut through a multi-byte rune leaves valid UTF-8", func(t *testing.T) {
		// Every rune is 3 bytes, so the byte limit lands mid-rune whatever it is.
		got := capture(t, errors.New(strings.Repeat("→", 10_000))).LastError
		if len(got) > maxLastErrorLength {
			t.Errorf("last_error is %d bytes, want at most %d", len(got), maxLastErrorLength)
		}
		if !utf8.ValidString(got) {
			t.Errorf("last_error is not valid UTF-8, so Postgres would reject the INSERT and the "+
				"dead letter would be lost entirely: %q", got)
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

// TestEnqueueReplayRoutesToARoleQueue is the regression test for a replay that
// landed on asynq's "default" queue.
//
// EnqueueReplay is the one producer that does not know what it is enqueuing —
// it hands back whatever was captured — and it used to set no queue at all.
// Every other producer routes, so the funnel guard never saw this one, and the
// control role does not consume "default": a replayed sweep or
// deliverability:evaluate could only be claimed by a send host, which has no
// handler for it and dead-letters it again. That is the exact failure this
// package's role queues exist to prevent, reproduced in the one API an
// operator uses to recover from it — and unrecoverably, because the replay
// claim is one-shot.
func TestEnqueueReplayRoutesToARoleQueue(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  ReplayJob
		want string
	}{
		// The captured queue wins: it is where the task ACTUALLY ran, and for a
		// warmup tick it names the worker — and therefore the IP — the mailbox
		// was warming from. Losing that during recovery is the worst moment to
		// move a warming mailbox.
		{"an affinity queue survives recovery", ReplayJob{TaskType: TaskWarmupTick, Queue: WorkerQueue("worker-a")}, WorkerQueue("worker-a")},
		{"a captured role queue is replayed onto as-is", ReplayJob{TaskType: TaskInboxPoll, Queue: QueueControl}, QueueControl},
		// No captured queue: a row written before the queue column existed.
		// The task type's own route is the reconstruction.
		{"per-message work", ReplayJob{TaskType: TaskInboxPoll}, QueueSend},
		{"a deferred manual reply", ReplayJob{TaskType: TaskInboxPendingReplySend}, QueueSend},
		{"the campaign breaker", ReplayJob{TaskType: TaskDeliverabilityEvaluate}, QueueControl},
		{"a periodic reconcile", ReplayJob{TaskType: TaskSweepEnrollments}, QueueControl},
		// "default" is drain-only and its consumption is being removed, so a
		// row captured off it must NOT be replayed back onto it — that is the
		// silent no-op this whole fix exists to remove, just one release later.
		{"the deprecated default is never replayed onto", ReplayJob{TaskType: TaskInboxPendingReplySend, Queue: QueueDefault}, QueueSend},
		// An arbitrary type is exactly what this path can be handed, and a
		// captured queue answers for it even though the lookup cannot.
		{"an unmapped type still replays where it ran", ReplayJob{TaskType: "some:unknown-task", Queue: QueueSend}, QueueSend},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeEnqueuer{}
			c := &Client{inner: fake}
			job := tc.job
			job.Payload = mustPayload(t, uuid.NewString())
			job.Key = "replay:1"
			if err := c.EnqueueReplay(context.Background(), job); err != nil {
				t.Fatalf("EnqueueReplay: %v", err)
			}
			if got, ok := fake.queue(); !ok || got != tc.want {
				t.Errorf("replayed %s to %q (present=%v), want %q", job.TaskType, got, ok, tc.want)
			}
			if fake.task == nil || fake.task.Type() != job.TaskType {
				t.Errorf("replay must re-enqueue the captured task type verbatim, got %v", fake.task)
			}
		})
	}
}

// A task type with no route is refused rather than enqueued, for the same
// reason c.enqueue refuses a task with no Queue option: landing on "default" is
// a task that runs only while the transitional drain survives, and a replay
// that no-ops has burned the row's single recovery attempt. The error names the
// type, because an arbitrary one is exactly what this path can be handed.
func TestEnqueueReplayRefusesAnUnroutableTaskType(t *testing.T) {
	fake := &fakeEnqueuer{}
	c := &Client{inner: fake}

	err := c.EnqueueReplay(context.Background(), ReplayJob{
		TaskType: "some:unknown-task",
		Payload:  mustPayload(t, uuid.NewString()),
		Key:      "replay:1",
	})
	if err == nil {
		t.Fatal("replay of an unroutable task type: got nil error, want one")
	}
	if !strings.Contains(err.Error(), "some:unknown-task") {
		t.Errorf("error %q does not name the task type", err.Error())
	}
	if fake.task != nil {
		t.Error("an unroutable replay must be refused before it reaches the queue, not after")
	}
}

// The queue a task died on is recorded, because nothing else on the row can
// reconstruct it. The task type gives the ROLE queue, which is right for
// everything except the one case where it matters most: warmup:tick is routed
// to its mailbox's "w:<id>" affinity queue so a warming mailbox keeps egressing
// from one IP, and that id appears nowhere in the payload.
//
// The empty case is a real one, not a defensive branch: asynq populates the
// queue name through an internal context package this module cannot import, so
// a synthetic context (and any pre-upgrade worker) yields none — and replay
// then falls back to the task type's route.
func TestCaptureRecordsTheQueueTheTaskRanOn(t *testing.T) {
	ws := uuid.New().String()
	for _, tc := range []struct {
		name  string
		queue string
	}{
		{"an affinity queue", WorkerQueue("worker-a")},
		{"a role queue", QueueSend},
		{"no queue on the context", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &fakeRecorder{}
			recordTerminalFailure(context.Background(), rec, quietLogger(),
				asynq.NewTask(TaskWarmupTick, mustPayload(t, ws)), errors.New("smtp: timeout"), tc.queue, 6, sendMaxRetry)

			if len(rec.calls) != 1 {
				t.Fatalf("recorded %d dead letters, want 1", len(rec.calls))
			}
			if rec.calls[0].Queue != tc.queue {
				t.Errorf("captured queue = %q, want %q", rec.calls[0].Queue, tc.queue)
			}
		})
	}
}
