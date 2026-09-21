package inbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/metrics"
)

// pendingSweepKind is the metric label for the stranded-manual-send sweep.
// Fixed, so the dimension stays bounded — and distinct from sweepKind ("inbox"),
// which labels the poll fan-out in this same package.
const pendingSweepKind = "inbox_pending_sends"

// DefaultPendingSweepWindow is how stale a pending manual send must be before
// the sweep decides nothing is going to move it.
//
// Both numbers are generous rather than tight, because the cost of waiting is
// bounded (one more tick) and the cost of being eager is noise on the queue
// during exactly the incident that produced the backlog.
//
//   - OverdueAfter 5m comfortably outlasts one task's whole asynq retry cycle
//     (sendMaxRetry attempts on an exponential backoff), so a reply that is
//     merely between retries — released to 'scheduled', waiting to be redelivered
//     — is left to the machinery that is already handling it.
//   - LeaseGrace 5m sits on TOP of the 300s lease the control plane adds, so a
//     'sending' row is nominated only ~10 minutes after it was claimed. Twice the
//     lease is far beyond any attempt that could still be running: asynq cancels
//     a pending-send handler at sendTimeout, two minutes.
//
// Worst case, then, a stranded reply leaves ~7 minutes late ('scheduled') or
// ~12 ('sending'). The number to compare that against is not zero, it is never.
var DefaultPendingSweepWindow = coreapi.StrandedPendingWindow{
	OverdueAfter: 5 * time.Minute,
	LeaseGrace:   5 * time.Minute,
}

// PendingSweepCore is the narrow coreapi capability the stranded-send sweep
// needs: name the rows, nothing more. Consumer-defined here rather than added to
// coreapi.Client — the same trade maintenance.Cleaner, recipientesp.Core and
// fleet.Rotator make, and for the same reason (that interface carries ~40
// methods and a dozen test fakes implement it in full).
//
// ONE METHOD, AND IT IS A READ. That is the interface-segregation point and also
// the safety point: this sweep is structurally incapable of claiming, releasing,
// marking or sending, so it cannot become a second delivery path for mail the
// ordinary handler already owns.
type PendingSweepCore interface {
	ListStrandedPendingInboxSends(ctx context.Context, w coreapi.StrandedPendingWindow) ([]coreapi.StrandedPendingSend, error)
}

// PendingSweepEnqueuer re-drives one stranded row by enqueuing the ORDINARY send
// task for it — the same task type, payload and handler the scheduled path uses.
// Satisfied by *queue.Client.
type PendingSweepEnqueuer interface {
	EnqueueStrandedPendingInboxReply(ctx context.Context, pendingID, workspaceID string) error
	EnqueueStrandedPendingInboxCompose(ctx context.Context, pendingID, workspaceID string) error
}

// PendingSweepHandler returns an asynq handler for inbox:pending_send_sweep
// tasks: the safety net under mail a HUMAN wrote and pressed send on.
//
// # What it is for
//
// Two ways a manual send stops making progress with nobody noticing, both of
// which leave the operator looking at an outbox row that appears healthy:
//
//   - The row is 'scheduled' and its task is GONE. The enqueue failed after the
//     row committed, Redis lost the entry, or the delayed task was dropped. The
//     row's own doc used to say a sweeper "does not exist yet, so this must not
//     pretend the row is safe on its own" (internal/app/inbox/pending.go). This
//     is that sweeper.
//   - The row is 'sending' with an abandoned lease. A worker claimed it and
//     died, or — the case slice 3b made reachable — the claim's response was lost
//     on the wire, taking the lease and the body with it. Until now only a task
//     attempt that happened to land after the lease expired could rescue it,
//     which made asynq's retry schedule the thing deciding whether a human's
//     reply is ever sent.
//
// # What makes a row safe to rescue
//
// Three things, and the third is the one that matters:
//
//  1. The scan's predicate is a strict SUBSET of ClaimInboxPendingReply's own
//     guard — 'scheduled' past send_after, or 'sending' past the lease PLUS a
//     grace the control plane adds the lease to itself (queries/inbox.sql,
//     inprocess.ListStrandedPendingInboxSends). A row under a LIVE lease is never
//     nominated, because a live lease means a worker may be mid-dial and the
//     lease is the only thing standing between two workers and one duplicate
//     reply.
//  2. The sweep MUTATES NOTHING. It does not release the row, does not clear the
//     lease, does not advance send_after. A row it rescues is one that was
//     already claimable; the sweep supplies the missing task, not a state change.
//     Releasing a 'sending' row here would actively destroy the evidence the
//     claim guard uses.
//  3. The sweep NEVER SENDS. It hands ids to the ordinary send task, whose first
//     act is the same guarded claim any other attempt makes. So the sweep cannot
//     produce a delivery the claim would have refused — including one racing a
//     worker that took the lease between the scan and the enqueue, which loses
//     the claim and returns nil.
//
// The residual risk is the one every at-least-once delivery system has and this
// sweep inherits rather than invents: a row abandoned in 'sending' AFTER its
// dial succeeded, because the post-dial MarkPendingInboxReplySent never
// committed (a control-plane outage in the instant after the provider ACK). The
// row cannot say whether it was dialed, so a rescue re-sends it. That window is
// invariant 4a's accepted posture applied to a path that previously had no
// attempt to reach it — the same trade sequence.SweepHandler already makes for
// campaign sends, taken here because a reply that never leaves is the worse
// outcome and the operator cannot tell it happened.
//
// # Bound
//
// One tick takes at most the scan's own LIMIT per table (queries/inbox.sql, 200
// each), oldest first. The remainder is not dropped and not starved: rescued
// rows leave the candidate set as they are claimed, so the next tick — two
// minutes later — takes the next oldest. A backlog costs ticks, never rows.
//
// mtx records the scan's duration and candidate count; a nil mtx no-ops. A
// failed scan is deliberately unrecorded, for the reason sequence.SweepHandler
// gives: a failed scan is not an observation, and folding its short duration
// into the histogram would drag the sweep's apparent cost down during exactly
// the incident an operator is looking at.
func PendingSweepHandler(
	core PendingSweepCore,
	enq PendingSweepEnqueuer,
	window coreapi.StrandedPendingWindow,
	mtx *metrics.Metrics,
) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		started := time.Now()
		stranded, err := core.ListStrandedPendingInboxSends(ctx, window)
		if err != nil {
			return err
		}
		mtx.SweepCompleted(pendingSweepKind, len(stranded), time.Since(started))
		if len(stranded) == 0 {
			return nil
		}

		var replies, composes, failures, unknown int
		for _, s := range stranded {
			if cancelled(ctx) {
				// Shutdown, or asynq's own task deadline. Stopping is not a
				// failure: the rows not reached were never enqueued, so they are
				// still stranded and the next tick takes them.
				break
			}
			var enqErr error
			switch s.Kind {
			case coreapi.StrandedPendingKindReply:
				replies++
				enqErr = enq.EnqueueStrandedPendingInboxReply(ctx, s.ID, s.WorkspaceID)
			case coreapi.StrandedPendingKindCompose:
				composes++
				enqErr = enq.EnqueueStrandedPendingInboxCompose(ctx, s.ID, s.WorkspaceID)
			default:
				// Unreachable from the in-process client, which mints both
				// constants itself. Logged rather than ignored because the way it
				// becomes reachable is a control plane newer than this worker
				// answering with a third kind, and silently skipping a human's
				// mail is the outcome this whole handler exists to prevent.
				unknown++
				slog.ErrorContext(ctx, "inbox_pending_send_sweep_unknown_kind",
					"kind", s.Kind, "pending_id", s.ID)
				continue
			}
			if enqErr != nil {
				// Not fatal to the tick and not retried here: the row is
				// untouched, so it is still stranded and the next tick re-nominates
				// it. Logged with the id (a row uuid, not content) so a persistent
				// Redis failure is attributable.
				failures++
				slog.ErrorContext(ctx, "inbox_pending_send_rescue_enqueue_failed",
					"kind", s.Kind, "pending_id", s.ID, "err", enqErr)
			}
		}
		slog.InfoContext(ctx, "inbox_pending_send_sweep",
			"candidates", len(stranded), "replies", replies, "composes", composes,
			"enqueue_failures", failures, "unknown_kind", unknown)
		return nil
	}
}

// cancelled reports whether the tick should stop early — a worker shutdown, or
// asynq's own task deadline. It mirrors recipientesp.cancelled rather than
// importing it (an unexported helper in a sibling worker package) and it is a
// select rather than a `ctx.Err() != nil`: the boolean is what this means. A
// context error observed and then not returned is exactly the shape `nilerr`
// flags, and it is right to — except here, where stopping is NOT a failed run.
// The rows not reached were never enqueued, so they are still stranded and the
// next tick takes them.
func cancelled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
