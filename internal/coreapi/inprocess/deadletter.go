package inprocess

import (
	"context"
	"fmt"
	"math"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/deadletter"
	"github.com/inroad/inroad/internal/coreapi"
)

// RecordDeadLetter persists one retry-exhausted task so an operator can see and
// replay it. This is the control-plane half of the capture path: the worker's
// asynq ErrorHandler (queue.DeadLetterErrorHandler) detects exhaustion and calls
// here, because the execution plane reaches relational data only through
// coreapi.
//
// The write goes through deadletter.Service rather than straight to the store so
// the boundary validation (a workspace and a task type are required, an empty
// payload normalises to JSON null) lives in ONE place and applies whether the
// row arrives from a worker or, in future, an HTTP coreapi.
func (c client) RecordDeadLetter(ctx context.Context, in coreapi.DeadLetterInput) error {
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return fmt.Errorf("coreapi: dead letter workspace id: %w", err)
	}
	_, err = deadLetterService(c).Capture(ctx, deadletter.Capture{
		WorkspaceID:  ws,
		TaskType:     in.TaskType,
		Payload:      in.Payload,
		LastError:    in.LastError,
		AttemptCount: clampAttemptCount(in.AttemptCount),
	})
	return err
}

// clampAttemptCount narrows asynq's int attempt counter to the column's int32
// without wrapping. A negative value is impossible from the capture path but
// would violate the table's CHECK (attempt_count >= 0) and fail the insert,
// losing the record over a diagnostic field — so it is floored rather than
// propagated.
func clampAttemptCount(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}

// deadLetterService builds the dead-letter service over this client's pool.
// Constructed per call, like replyLabelService and the CRM service: it is
// stateless and the pool is its only dependency, so caching it would buy
// nothing but a lifetime to manage.
//
// The Enqueuer is nil here on purpose. This path only ever CAPTURES; replay is
// an operator action on the control plane, and a nil queue seam makes it
// structurally impossible for the execution plane to re-enqueue anything
// through this client.
func deadLetterService(c client) *deadletter.Service {
	return deadletter.NewService(deadletter.NewPgStore(c.q), nil)
}

var _ coreapi.DeadLetterClient = client{}
