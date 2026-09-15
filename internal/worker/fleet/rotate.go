// Package fleet owns the execution-plane trigger for the control plane's fleet
// decisions. Today that is one handler: the periodic rotation pass.
//
// It holds no policy and makes no decision. Rotation scans assignments across
// every workspace, scores destinations and writes both the move and its
// decision-log entry — all of which is relational work the execution plane
// reaches only through coreapi (there is a depguard rule), so the whole pass
// lives behind one method and this package's job is to call it on a schedule and
// report what happened.
package fleet

import (
	"context"
	"log/slog"

	"github.com/hibiken/asynq"
)

// Rotator is the narrow, consumer-defined capability this handler needs.
// Deliberately one method, like maintenance.Cleaner and deliverability.Breaker:
// coreapi.Client already carries ~40 methods that a dozen test fakes implement
// in full, and widening it for one scheduled job would break every one of them
// for no gain. A Client that does not implement this simply has no rotation,
// which is exactly the behaviour that shipped before it existed — and which a
// future HTTP coreapi will report until it grows the endpoint.
type Rotator interface {
	// RotateMailboxWorkers runs one pass and reports how many mailboxes moved.
	// Zero is the normal answer, including on every self-host deployment, where
	// there is no second worker to rotate to.
	RotateMailboxWorkers(ctx context.Context) (moved int64, err error)
}

// RotateHandler runs one rotation pass per task.
//
// A failed pass is returned so asynq retries it: rotation is a reconcile, its
// partial progress is durable (each move is its own guarded UPDATE), and a
// retry re-reads the fleet rather than replaying a stale plan.
//
// The count is logged at INFO even when it is zero. "Is rotation running, and is
// it moving anything?" is the first question an operator asks about fleet churn,
// and a log line that only appears when something moved cannot distinguish a
// quiet fleet from a job that stopped firing.
func RotateHandler(core Rotator) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		moved, err := core.RotateMailboxWorkers(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "fleet rotation pass complete", "mailboxes_moved", moved)
		return nil
	}
}
