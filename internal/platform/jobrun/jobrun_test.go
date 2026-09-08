package jobrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

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

// 6. scheduled_job_runs has no workspace_id and no CHECK on error_message
// length, so whatever a sweep's error string happens to contain is what lands
// in an instance-scoped table that is read by an operator, not a tenant. The
// column comment claimed "never tenant content", which the decorator cannot
// promise: it stores errors.Error() from six handlers it does not own, and one
// of them wrapping a mailbox address or a recipient list would make the claim
// false. Capping is the guarantee that can actually be kept.
func TestRecordCapsAnUnboundedErrorMessage(t *testing.T) {
	rec := &fakeRecorder{}
	long := strings.Repeat("a", 10<<10)
	handler := func(context.Context, *asynq.Task) error { return errors.New(long) }

	if err := Record(rec, nil, "widget sweep", handler)(context.Background(), task()); err == nil {
		t.Fatal("the handler's error must still be returned to asynq")
	}
	got := rec.calls[0].ErrorMessage
	if len(got) > maxErrorMessageBytes {
		t.Fatalf("ErrorMessage is %d bytes, want at most %d", len(got), maxErrorMessageBytes)
	}
	if !strings.HasSuffix(got, truncatedErrorMarker) {
		t.Errorf("a capped message must say so, got the tail %q", got[max(0, len(got)-40):])
	}
	if !strings.HasPrefix(got, "aaaa") {
		t.Errorf("the start of the error is the diagnostic part and must be kept, got %q", got[:20])
	}
}

// A message at or under the cap is stored verbatim: the cap must not put a
// truncation marker on an error that was never truncated.
func TestRecordLeavesAShortErrorMessageAlone(t *testing.T) {
	rec := &fakeRecorder{}
	handler := func(context.Context, *asynq.Task) error { return errors.New("dial tcp: connection refused") }

	_ = Record(rec, nil, "widget sweep", handler)(context.Background(), task())
	if got := rec.calls[0].ErrorMessage; got != "dial tcp: connection refused" {
		t.Fatalf("ErrorMessage = %q, want it verbatim", got)
	}
}

// The cap cuts BYTES, so it must cut on a rune boundary. Half a multi-byte rune
// is invalid UTF-8, which Postgres refuses outright (SQLSTATE 22021) — the cap
// would then turn a long error into a FAILED ledger write, which is strictly
// worse than the unbounded column it replaced.
func TestRecordCapCutsOnARuneBoundary(t *testing.T) {
	rec := &fakeRecorder{}
	// Three-byte runes do not divide the cap evenly, so a naive byte slice
	// lands mid-rune.
	handler := func(context.Context, *asynq.Task) error {
		return errors.New(strings.Repeat("あ", 10<<10))
	}

	_ = Record(rec, nil, "widget sweep", handler)(context.Background(), task())
	got := rec.calls[0].ErrorMessage
	if !utf8.ValidString(got) {
		t.Fatalf("capped message is not valid UTF-8: %q", got)
	}
	if len(got) > maxErrorMessageBytes {
		t.Fatalf("ErrorMessage is %d bytes, want at most %d", len(got), maxErrorMessageBytes)
	}
}
