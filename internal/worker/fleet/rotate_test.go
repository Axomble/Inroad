package fleet

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
)

// stubRotator answers with whatever the test wants and counts the calls.
type stubRotator struct {
	moved int64
	err   error
	calls int
}

func (s *stubRotator) RotateMailboxWorkers(context.Context) (int64, error) {
	s.calls++
	return s.moved, s.err
}

// One task, one pass. The handler owns no policy, so the only thing it can get
// wrong is how many times it asks — and asking twice would double a tick's move
// budget, which is the one bound rotation has against churn.
func TestOneTaskRunsExactlyOnePass(t *testing.T) {
	core := &stubRotator{moved: 3}
	if err := RotateHandler(core)(context.Background(), asynq.NewTask("fleet:rotate", nil)); err != nil {
		t.Fatalf("RotateHandler: %v", err)
	}
	if core.calls != 1 {
		t.Fatalf("ran %d passes for one task, want 1", core.calls)
	}
}

// A failed pass propagates, so asynq retries it. Swallowing it would leave a
// blocked worker holding its mailboxes until the next tick with nothing in the
// run ledger to say the pass had failed.
func TestAFailedPassIsReturnedSoTheTaskRetries(t *testing.T) {
	sentinel := errors.New("fleet unavailable")
	err := RotateHandler(&stubRotator{err: sentinel})(context.Background(), asynq.NewTask("fleet:rotate", nil))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
}

// Moving nothing is the normal outcome — every self-host deployment, and every
// healthy fleet — and must not read as a failure.
func TestAPassThatMovesNothingSucceeds(t *testing.T) {
	if err := RotateHandler(&stubRotator{})(context.Background(), asynq.NewTask("fleet:rotate", nil)); err != nil {
		t.Fatalf("a pass that moved nothing must succeed, got %v", err)
	}
}
