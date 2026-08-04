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
	deleted            int64
	err                error
	idempotencyDeleted int64
	idempotencyErr     error
	called             bool
	idempotencyCalled  bool
}

func (c *cleanupCore) CleanupExpired(context.Context) (int64, error) {
	c.called = true
	return c.deleted, c.err
}

func (c *cleanupCore) PurgeIdempotencyKeys(context.Context) (int64, error) {
	c.idempotencyCalled = true
	return c.idempotencyDeleted, c.idempotencyErr
}

func TestCleanupHandler(t *testing.T) {
	core := &cleanupCore{deleted: 12, idempotencyDeleted: 3}
	if err := CleanupHandler(core)(context.Background(), asynq.NewTask(queue.TaskMaintenanceCleanup, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !core.called {
		t.Fatal("CleanupExpired was not called")
	}
	if !core.idempotencyCalled {
		t.Fatal("PurgeIdempotencyKeys was not called")
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
