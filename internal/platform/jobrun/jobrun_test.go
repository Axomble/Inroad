package jobrun

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
)

// fakeRecorder captures every RecordJobRun call so a test can assert exactly
// what Record persisted, and can be told to fail the call itself (test 4).
type fakeRecorder struct {
	calls []Run
	err   error
}

func (f *fakeRecorder) RecordJobRun(_ context.Context, run Run) error {
	f.calls = append(f.calls, run)
	return f.err
}

func task() *asynq.Task { return asynq.NewTask("test:task", nil) }

// 1. A handler returning nil records outcome ok.
func TestRecordSuccessRecordsOutcomeOK(t *testing.T) {
	rec := &fakeRecorder{}
	handler := func(context.Context, *asynq.Task) error { return nil }

	if err := Record(rec, nil, "widget sweep", handler)(context.Background(), task()); err != nil {
		t.Fatalf("wrapped handler returned %v, want nil", err)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("RecordJobRun called %d times, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	if got.Name != "widget sweep" {
		t.Errorf("Name = %q, want %q", got.Name, "widget sweep")
	}
	if got.Outcome != OutcomeOK {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeOK)
	}
	if got.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q, want empty on success", got.ErrorMessage)
	}
	if got.FinishedAt.Before(got.StartedAt) {
		t.Errorf("FinishedAt %v is before StartedAt %v", got.FinishedAt, got.StartedAt)
	}
}

// 2. A handler returning an error records outcome error WITH the message.
func TestRecordFailureRecordsOutcomeErrorWithMessage(t *testing.T) {
	rec := &fakeRecorder{}
	wantErr := errors.New("dial timeout: no route to host")
	handler := func(context.Context, *asynq.Task) error { return wantErr }

	err := Record(rec, nil, "domain auth sweep", handler)(context.Background(), task())
	if !errors.Is(err, wantErr) {
		t.Fatalf("wrapped handler returned %v, want %v", err, wantErr)
	}

	if len(rec.calls) != 1 {
		t.Fatalf("RecordJobRun called %d times, want 1", len(rec.calls))
	}
	got := rec.calls[0]
	if got.Outcome != OutcomeError {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeError)
	}
	if got.ErrorMessage != wantErr.Error() {
		t.Errorf("ErrorMessage = %q, want %q", got.ErrorMessage, wantErr.Error())
	}
}

// 3. A handler that panics records a row AND the panic still propagates. A
// swallowed panic would turn a crash into silence, which is strictly worse
// than the crash — see Record's own doc.
func TestRecordPanicRecordsRowAndRepanics(t *testing.T) {
	rec := &fakeRecorder{}
	handler := func(context.Context, *asynq.Task) error { panic("catastrophic failure") }

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the panic to propagate out of the wrapped handler, but it did not")
		}
		if r != "catastrophic failure" {
			t.Errorf("recovered panic value = %v, want %q", r, "catastrophic failure")
		}
		if len(rec.calls) != 1 {
			t.Fatalf("RecordJobRun called %d times, want 1", len(rec.calls))
		}
		got := rec.calls[0]
		if got.Outcome != OutcomeError {
			t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeError)
		}
		if got.ErrorMessage != "panic: catastrophic failure" {
			t.Errorf("ErrorMessage = %q, want %q", got.ErrorMessage, "panic: catastrophic failure")
		}
	}()

	_ = Record(rec, nil, "warmup sweep", handler)(context.Background(), task())
	t.Fatal("Record did not panic — the wrapped handler's panic was swallowed")
}

// 4. A recorder that itself errors does not change the wrapped handler's
// return value. Observability must never break the thing it observes.
func TestRecordRecorderFailureDoesNotChangeHandlerResult(t *testing.T) {
	rec := &fakeRecorder{err: errors.New("scheduled_job_runs insert: connection refused")}
	handler := func(context.Context, *asynq.Task) error { return nil }

	if err := Record(rec, nil, "recipient esp sweep", handler)(context.Background(), task()); err != nil {
		t.Fatalf("wrapped handler returned %v, want nil (the recorder's own failure must not surface here)", err)
	}

	wantErr := errors.New("boom")
	handler2 := func(context.Context, *asynq.Task) error { return wantErr }
	if err := Record(rec, nil, "recipient esp sweep", handler2)(context.Background(), task()); !errors.Is(err, wantErr) {
		t.Fatalf("wrapped handler returned %v, want %v (the recorder's own failure must not mask the real one)", err, wantErr)
	}
}

// 5. A coreapi client without the Recorder capability — modeled here as a nil
// Recorder, exactly what handlers.go's failed type assertion yields — is a
// no-op, and the wrapped handler still runs.
func TestRecordNilRecorderIsNoOpAndHandlerStillRuns(t *testing.T) {
	var ran bool
	handler := func(context.Context, *asynq.Task) error {
		ran = true
		return nil
	}

	if err := Record(nil, nil, "maintenance cleanup", handler)(context.Background(), task()); err != nil {
		t.Fatalf("wrapped handler returned %v, want nil", err)
	}
	if !ran {
		t.Fatal("the wrapped handler did not run when recorder was nil")
	}
}
