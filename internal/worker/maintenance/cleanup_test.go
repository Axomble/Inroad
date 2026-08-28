package maintenance

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

type cleanupCore struct {
	coreapi.Client
	deleted             int64
	err                 error
	idempotencyDeleted  int64
	idempotencyErr      error
	observationsDeleted int64
	observationsErr     error
	workersDeleted      int64
	workersErr          error
	deadLettersDeleted  int64
	deadLettersErr      error
	called              bool
	idempotencyCalled   bool
	observationsCalled  bool
	workersCalled       bool
	deadLettersCalled   bool
}

func (c *cleanupCore) CleanupExpired(context.Context) (int64, error) {
	c.called = true
	return c.deleted, c.err
}

func (c *cleanupCore) PurgeIdempotencyKeys(context.Context) (int64, error) {
	c.idempotencyCalled = true
	return c.idempotencyDeleted, c.idempotencyErr
}

func (c *cleanupCore) PurgeWarmupObservations(context.Context) (int64, error) {
	c.observationsCalled = true
	return c.observationsDeleted, c.observationsErr
}

func (c *cleanupCore) PurgeDeadWorkers(context.Context) (int64, error) {
	c.workersCalled = true
	return c.workersDeleted, c.workersErr
}

func (c *cleanupCore) PurgeDeadLetters(context.Context) (int64, error) {
	c.deadLettersCalled = true
	return c.deadLettersDeleted, c.deadLettersErr
}

func TestCleanupHandler(t *testing.T) {
	core := &cleanupCore{deleted: 12, idempotencyDeleted: 3, observationsDeleted: 7, workersDeleted: 2, deadLettersDeleted: 4}
	if err := CleanupHandler(core)(context.Background(), asynq.NewTask(queue.TaskMaintenanceCleanup, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !core.called {
		t.Fatal("CleanupExpired was not called")
	}
	if !core.idempotencyCalled {
		t.Fatal("PurgeIdempotencyKeys was not called")
	}
	if !core.observationsCalled {
		t.Fatal("PurgeWarmupObservations was not called")
	}
	if !core.workersCalled {
		t.Fatal("PurgeDeadWorkers was not called")
	}
	if !core.deadLettersCalled {
		t.Fatal("PurgeDeadLetters was not called")
	}
}

// task_dead_letters had no retention sweep before this and grows with failures
// nobody schedules, so a purge that silently stopped running would be invisible
// until the table was a problem. The failure surfaces for retry instead.
func TestCleanupHandlerReturnsErrorOnDeadLetterPurgeFailure(t *testing.T) {
	want := errors.New("db unavailable")
	core := &cleanupCore{deadLettersErr: want}
	err := CleanupHandler(core)(context.Background(), asynq.NewTask(queue.TaskMaintenanceCleanup, nil))
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want %v", err, want)
	}
	if !core.workersCalled {
		t.Fatal("the earlier purges should still have run")
	}
}

// A dead worker's rows are inert for routing (the assigner's liveness join
// already ignores them) but they keep inflating that worker's count in the
// least-loaded pick, so a failed reap has to surface for retry rather than
// quietly skewing assignment balance forever.
func TestCleanupHandlerReturnsErrorOnDeadWorkerPurgeFailure(t *testing.T) {
	want := errors.New("db unavailable")
	core := &cleanupCore{workersErr: want}
	err := CleanupHandler(core)(context.Background(), asynq.NewTask(queue.TaskMaintenanceCleanup, nil))
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want %v", err, want)
	}
}

// Warmup evidence is reachable by any external sender (a forged token on inbound
// mail writes an observer-side row), so a failure to purge it must surface for
// retry rather than letting the table grow unbounded in silence.
func TestCleanupHandlerReturnsErrorOnObservationPurgeFailure(t *testing.T) {
	want := errors.New("db unavailable")
	core := &cleanupCore{observationsErr: want}
	err := CleanupHandler(core)(context.Background(), asynq.NewTask(queue.TaskMaintenanceCleanup, nil))
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want %v", err, want)
	}
}

func TestCleanupHandlerReturnsErrorForRetry(t *testing.T) {
	want := errors.New("db unavailable")
	core := &cleanupCore{err: want}
	err := CleanupHandler(core)(context.Background(), asynq.NewTask(queue.TaskMaintenanceCleanup, nil))
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want %v", err, want)
	}
	if core.idempotencyCalled {
		t.Fatal("PurgeIdempotencyKeys must not run when CleanupExpired already failed")
	}
	if core.observationsCalled {
		t.Fatal("PurgeWarmupObservations must not run when CleanupExpired already failed")
	}
	if core.deadLettersCalled {
		t.Fatal("PurgeDeadLetters must not run when CleanupExpired already failed")
	}
}

func TestCleanupHandlerReturnsErrorForRetryOnIdempotencyPurgeFailure(t *testing.T) {
	want := errors.New("db unavailable")
	core := &cleanupCore{deleted: 5, idempotencyErr: want}
	err := CleanupHandler(core)(context.Background(), asynq.NewTask(queue.TaskMaintenanceCleanup, nil))
	if !errors.Is(err, want) {
		t.Fatalf("handler error = %v, want %v", err, want)
	}
	if !core.called {
		t.Fatal("CleanupExpired should still have run before the idempotency purge failed")
	}
}
