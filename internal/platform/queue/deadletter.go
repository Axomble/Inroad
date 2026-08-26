package queue

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/bus"
)

// DeadLetterRecorder is the capture seam this file publishes tasks into — one
// method, defined here at the boundary because platform/* must never import
// app/*. The execution plane satisfies it through coreapi (whose in-process
// implementation delegates to app/deadletter.Service.Capture); a nil recorder
// disables capture entirely, which is what a deployment without the control
// plane wired gets.
//
// The payload is handed over as raw bytes. This package deliberately does not
// interpret a task's body beyond extracting the workspace id (see
// workspaceFromPayload): every task type has its own shape, and a capture path
// that had to know all of them would break the moment a handler was added.
type DeadLetterRecorder interface {
	RecordDeadLetter(ctx context.Context, in DeadLetter) error
}

// DeadLetter is one retry-exhausted task, as observed at asynq's terminal
// failure. WorkspaceID is a string rather than a uuid.UUID because this package
// reads it out of an opaque JSON payload; the app layer parses and validates it.
type DeadLetter struct {
	WorkspaceID  string
	TaskType     string
	Payload      []byte
	LastError    string
	AttemptCount int
}

// DeadLetterErrorHandler returns an asynq.ErrorHandler that records a task in
// the dead-letter table when — and only when — it has exhausted its retries.
//
// WHY AN ErrorHandler RATHER THAN PER-HANDLER DETECTION: asynq calls this for
// EVERY failed attempt, and the context it passes carries the attempt counters
// (GetRetryCount / GetMaxRetry). That makes exhaustion a property this one
// function can observe for every task type at once. Detecting it inside each
// handler would mean every handler re-deriving the same condition, every new
// handler being a chance to forget it, and — worse — a handler that PANICS or
// times out never getting the chance, since asynq recovers those itself and
// routes them here all the same.
//
// The retry-count comparison is asynq's documented exhaustion idiom: retryCount
// is the number of retries ALREADY made, so the attempt that fails when
// retryCount has reached maxRetry is the last one and the task is archived
// rather than rescheduled. A task explicitly skipped (asynq.SkipRetry) is also
// terminal and is captured for the same reason: nothing will run it again.
//
// Capture failures are logged, never propagated: asynq discards an
// ErrorHandler's outcome anyway, and the alternative — panicking inside the
// failure path — would take the worker down at the exact moment it is already
// having trouble.
func DeadLetterErrorHandler(recorder DeadLetterRecorder, logger *slog.Logger) asynq.ErrorHandler {
	return asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, taskErr error) {
		if recorder == nil || task == nil {
			return
		}
		retried, maxRetry, terminal := isTerminalFailure(ctx, taskErr)
		if !terminal {
			return
		}

		workspaceID, ok := workspaceFromPayload(task.Payload())
		if !ok {
			// Nothing can own this row and nothing could ever list or replay
			// it, so storing it would be worse than useless: it would be an
			// unreachable row on a tenant-scoped table. The task type and the
			// error are still worth an operator's attention, so they are
			// logged loudly. The PAYLOAD is deliberately absent from the log —
			// task payloads carry reply bodies and recipient addresses.
			logger.ErrorContext(ctx, "task exhausted retries but carries no workspace; not captured",
				"task_type", task.Type(), "attempts", retried+1, "err", taskErr)
			return
		}

		in := DeadLetter{
			WorkspaceID:  workspaceID,
			TaskType:     task.Type(),
			Payload:      task.Payload(),
			LastError:    errorMessage(taskErr),
			AttemptCount: retried + 1, // retries made, plus the attempt that just failed
		}
		if err := recorder.RecordDeadLetter(ctx, in); err != nil {
			logger.ErrorContext(ctx, "failed to record dead letter",
				"task_type", task.Type(), "workspace_id", workspaceID,
				"attempts", in.AttemptCount, "err", err)
			return
		}
		logger.WarnContext(ctx, "task exhausted retries and was dead-lettered",
			"task_type", task.Type(), "workspace_id", workspaceID,
			"attempts", in.AttemptCount, "max_retry", maxRetry, "err", taskErr)
	})
}

// isTerminalFailure reports whether this failure is the LAST one for the task —
// i.e. asynq will archive it rather than reschedule. It returns the counters
// alongside so the caller can record them without re-reading the context.
//
// Two independent ways a task becomes terminal:
//   - retries are exhausted (retryCount has reached maxRetry);
//   - the handler asked for no retry at all (asynq.SkipRetry).
//
// A context carrying no counters at all (ok=false from both) is treated as NOT
// terminal. That is the conservative direction: asynq always populates them for
// a real processed task, so their absence means this was not one (a synthetic
// call, a test), and capturing then would write rows for failures that will in
// fact be retried.
func isTerminalFailure(ctx context.Context, taskErr error) (retried, maxRetry int, terminal bool) {
	retried, retryOK := asynq.GetRetryCount(ctx)
	maxRetry, maxOK := asynq.GetMaxRetry(ctx)
	if !retryOK || !maxOK {
		return retried, maxRetry, false
	}
	return retried, maxRetry, isLastAttempt(retried, maxRetry, taskErr)
}

// isLastAttempt is the exhaustion decision itself, split out from the context
// plumbing so it can be tested at its boundary — asynq populates the counters
// through an internal package this module cannot import, so without this split
// the off-by-one that matters most here would be untestable.
//
// An off-by-one in either direction is a real bug: too strict and a genuinely
// lost send is never captured (the failure this ticket exists to fix); too
// loose and every transient blip writes a row claiming a task was dropped when
// it is about to be retried successfully.
func isLastAttempt(retried, maxRetry int, taskErr error) bool {
	// SkipRetry (archive now) and RevokeTask (drop now) both mean nothing will
	// run this task again, regardless of how many retries remain.
	if errors.Is(taskErr, asynq.SkipRetry) || errors.Is(taskErr, asynq.RevokeTask) {
		return true
	}
	// retried counts retries ALREADY made, so once it reaches maxRetry the
	// attempt that just failed was the last one asynq will schedule.
	return retried >= maxRetry
}

// workspaceFromPayload extracts the tenant from an opaque task payload. Every
// capturable payload in this package carries a workspace_id for exactly this
// reason (it is the defense-in-depth pin the worker's own coreapi lookups use),
// so this needs no per-task-type knowledge.
//
// ok=false for a payload that is not a JSON object, carries no workspace_id, or
// carries an empty one — the sweep tasks (inbox:sweep, warmup:sweep) have nil
// payloads and land here, which is correct: a fan-out task owns no tenant's
// work and re-running it is the scheduler's job, not an operator's.
func workspaceFromPayload(payload []byte) (string, bool) {
	if len(payload) == 0 {
		return "", false
	}
	var envelope struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return "", false
	}
	if envelope.WorkspaceID == "" {
		return "", false
	}
	return envelope.WorkspaceID, true
}

// errorMessage renders the failure for storage. A nil error is possible in
// principle (asynq's interface does not forbid it) and must not panic here.
func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// EnqueueReplay puts a captured payload back on the queue under the caller's
// deterministic key. It satisfies app/deadletter.Enqueuer.
//
// The task type and payload are replayed VERBATIM — a replay must re-run the
// original work, not a reinterpretation of it, so this deliberately does not
// route through the typed helpers above (which would rebuild the payload and
// could drift from what was captured).
//
// key becomes the asynq TaskID, and c.enqueue already treats a TaskID conflict
// as success: a duplicate replay of the same row that reaches this far collapses
// to one task rather than erroring. That is the SECOND line of defense only —
// the row claim in deadletter.Service.Replay is the durable one, because asynq
// reserves a task id for its retention window and no longer.
//
// Retries and timeout match a send's (sendMaxRetry/sendTimeout): a replayed task
// deserves the same attempts the original had, and a replay that exhausts them
// again simply dead-letters again, which is the correct outcome — the operator
// learns the task is not merely unlucky.
func (c *Client) EnqueueReplay(ctx context.Context, taskType string, payload []byte, key string) error {
	return c.Publish(ctx, bus.Job{
		Kind:    taskType,
		Payload: payload,
		Key:     key,
	}, bus.Options{
		MaxRetry: sendMaxRetry,
		Timeout:  sendTimeout,
	})
}
