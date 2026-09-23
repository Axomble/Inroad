package inprocess

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// The recipient-data retention batches (maintenance.Retainer). Each method is one
// statement from queries/retention.sql or deadletter.sql, run in its own short
// transaction, and every policy decision (what to keep, why) lives in that SQL,
// beside the guards it states. What is here is the translation to and from the
// seam types, the per-batch statement budget, and the sweep's run-level lock and
// cursor — written once (retentionBatch) so the five batches cannot disagree on
// how a window or a cursor is encoded, or on refusing a non-positive window.
//
// None of these is on the remote transport; see coreapi/retention.go.

// retentionStatementTimeout bounds ONE batch. The whole run is bounded by the
// worker (maintenance.RetentionOptions.Budget, 5 minutes) and killed by asynq at
// queue.sweepTimeout (10 minutes); this is the per-statement backstop underneath
// both, so a batch whose plan went wrong — or that queued behind a lock — fails
// on its own after a minute instead of holding its locks, and pinning the xmin
// horizon for every other table's vacuum, until the task is killed. A healthy
// batch is well under a second.
const retentionStatementTimeout = "60s"

// retentionParams is the parameter shape every retention batch query shares.
// sqlc generates one params struct per query, all with these four fields in this
// order, so each converts to it directly (Go ignores struct tags in a
// conversion) — and a query whose parameters drifted would stop compiling here
// rather than binding the wrong value.
type retentionParams struct {
	OlderThanSeconds int64
	AfterAt          pgtype.Timestamptz
	AfterID          uuid.UUID
	BatchLimit       int32
}

// encodeRetention validates a request at the seam and encodes it. A window of
// zero or less is refused rather than passed through: `now() - 0s` is "every row
// in the table", and the only thing standing between a disabled policy and a
// full-table delete must not be every caller remembering to check. The sweep
// never sends one (a disabled table is skipped before any call); this is the
// backstop for the caller that would.
func encodeRetention(req coreapi.RetentionRequest) (retentionParams, error) {
	if req.OlderThan <= 0 {
		return retentionParams{}, fmt.Errorf("retention: window must be positive, got %s", req.OlderThan)
	}
	if req.Limit <= 0 {
		return retentionParams{}, fmt.Errorf("retention: batch limit must be positive, got %d", req.Limit)
	}
	return retentionParams{
		// Truncated, not rounded: a fractional second can only make the cutoff a
		// hair later, i.e. keep slightly MORE, which is the safe direction.
		OlderThanSeconds: int64(req.OlderThan / time.Second),
		// Valid even for the zero cursor: a NULL in the row comparison would
		// match nothing, and the first batch would delete nothing.
		AfterAt:    pgtype.Timestamptz{Time: req.After.At, Valid: true},
		AfterID:    req.After.ID,
		BatchLimit: req.Limit,
	}, nil
}

func decodeCursor(at pgtype.Timestamptz, id uuid.UUID) coreapi.RetentionCursor {
	return coreapi.RetentionCursor{At: at.Time, ID: id}
}

// retentionBatch is the one shape all five share: validate and encode, run the
// statement in a transaction with SET LOCAL statement_timeout, translate its
// row. SET LOCAL needs the transaction — it would otherwise leak onto a pooled
// connection — and scopes the timeout to this batch alone.
func retentionBatch[R any](ctx context.Context, c client, req coreapi.RetentionRequest, what string,
	query func(context.Context, *gen.Queries, retentionParams) (R, error), result func(R) coreapi.RetentionBatch) (batch coreapi.RetentionBatch, err error) {
	p, err := encodeRetention(req)
	if err != nil {
		return coreapi.RetentionBatch{}, err
	}
	if c.pool == nil {
		return coreapi.RetentionBatch{}, errors.New("retention: no database pool")
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return coreapi.RetentionBatch{}, fmt.Errorf("%s: begin: %w", what, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, rollbackUnlessDone(ctx, tx))
		}
	}()
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '"+retentionStatementTimeout+"'"); err != nil {
		return coreapi.RetentionBatch{}, fmt.Errorf("%s: statement timeout: %w", what, err)
	}
	row, err := query(ctx, c.q.WithTx(tx), p)
	if err != nil {
		return coreapi.RetentionBatch{}, fmt.Errorf("%s: %w", what, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return coreapi.RetentionBatch{}, fmt.Errorf("%s: commit: %w", what, err)
	}
	return result(row), nil
}

// rollbackUnlessDone rolls back a transaction that did not commit. A rollback
// after a successful commit reports pgx.ErrTxClosed, which is not a failure.
func rollbackUnlessDone(ctx context.Context, tx pgx.Tx) error {
	// The rollback must happen even when ctx is what failed the batch: a
	// cancelled context would otherwise leave the transaction open on a pooled
	// connection until the pool noticed.
	if err := tx.Rollback(context.WithoutCancel(ctx)); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return fmt.Errorf("rollback: %w", err)
	}
	return nil
}

// RollupTrackingEvents folds one batch of raw tracking events past the window
// into tracking_event_rollups and deletes them, in one statement. There is no
// guard, so every row scanned is a row deleted.
func (c client) RollupTrackingEvents(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, c, req, "roll up tracking events",
		func(ctx context.Context, q *gen.Queries, p retentionParams) (gen.RollupTrackingEventsRow, error) {
			return q.RollupTrackingEvents(ctx, gen.RollupTrackingEventsParams(p))
		},
		func(r gen.RollupTrackingEventsRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Scanned: r.DeletedRows, Deleted: r.DeletedRows, RolledUp: r.RollupRows, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// PurgeDeliverabilityEvents deletes one batch of bounce/complaint events past the
// window that no rate still reads.
func (c client) PurgeDeliverabilityEvents(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, c, req, "purge deliverability events",
		func(ctx context.Context, q *gen.Queries, p retentionParams) (gen.PurgeDeliverabilityEventsRow, error) {
			return q.PurgeDeliverabilityEvents(ctx, gen.PurgeDeliverabilityEventsParams(p))
		},
		func(r gen.PurgeDeliverabilityEventsRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Scanned: r.ScannedRows, Deleted: r.DeletedRows, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// PurgeInboxThreads deletes one batch of whole inbox conversations with no
// activity inside the window, and their messages.
func (c client) PurgeInboxThreads(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, c, req, "purge inbox threads",
		func(ctx context.Context, q *gen.Queries, p retentionParams) (gen.PurgeInboxThreadsRow, error) {
			return q.PurgeInboxThreads(ctx, gen.PurgeInboxThreadsParams(p))
		},
		func(r gen.PurgeInboxThreadsRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Scanned: r.ScannedRows, Deleted: r.DeletedRows, Dependents: r.DeletedMessages, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// PurgeSends deletes one batch of campaign sends past the window that nothing
// live depends on, with their tracking events and rollups.
func (c client) PurgeSends(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, c, req, "purge sends",
		func(ctx context.Context, q *gen.Queries, p retentionParams) (gen.PurgeSendsRow, error) {
			return q.PurgeSends(ctx, gen.PurgeSendsParams(p))
		},
		func(r gen.PurgeSendsRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Scanned: r.ScannedRows, Deleted: r.DeletedRows, Dependents: r.DeletedTracking, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// PurgeDeadLetters deletes one batch of captured retry-exhausted tasks past the
// window. task_dead_letters is append-only in practice — triage flips a status,
// it never deletes — and had no sweep at all, so it grew forever on a system
// whose failure mode is a provider outage failing hundreds of queued sends at
// once (invariant 55's reasoning). It moved here from the daily maintenance
// cleanup, where its 90 days were hard-coded, so the window is the operator's
// (INROAD_RETENTION_DEAD_LETTERS_DAYS, default 90). No guard, so every row
// scanned is a row deleted.
func (c client) PurgeDeadLetters(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, c, req, "purge dead letters",
		func(ctx context.Context, q *gen.Queries, p retentionParams) (gen.PurgeTaskDeadLettersRow, error) {
			return q.PurgeTaskDeadLetters(ctx, gen.PurgeTaskDeadLettersParams(p))
		},
		func(r gen.PurgeTaskDeadLettersRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Scanned: r.DeletedRows, Deleted: r.DeletedRows, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// LoadRetentionProgress reads where a table's sweep got to. A table never swept
// has no row, which is the zero progress: start from the oldest row.
func (c client) LoadRetentionProgress(ctx context.Context, table string) (coreapi.RetentionProgress, error) {
	row, err := c.q.GetRetentionCursor(ctx, table)
	if errors.Is(err, pgx.ErrNoRows) {
		return coreapi.RetentionProgress{}, nil
	}
	if err != nil {
		return coreapi.RetentionProgress{}, fmt.Errorf("load retention cursor %s: %w", table, err)
	}
	return coreapi.RetentionProgress{Cursor: decodeCursor(row.AfterAt, row.AfterID), CycleExpired: row.CycleExpired}, nil
}

// SaveRetentionProgress records a table's position. newCycle restarts the
// day-long cycle clock; the sweep sets it whenever it starts over from the
// oldest row.
func (c client) SaveRetentionProgress(ctx context.Context, table string, cursor coreapi.RetentionCursor, newCycle bool) error {
	if err := c.q.SaveRetentionCursor(ctx, gen.SaveRetentionCursorParams{
		TableName: table,
		AfterAt:   pgtype.Timestamptz{Time: cursor.At, Valid: true},
		AfterID:   cursor.ID,
		NewCycle:  newCycle,
	}); err != nil {
		return fmt.Errorf("save retention cursor %s: %w", table, err)
	}
	return nil
}

// TryRetentionSweepLock takes the deployment-wide single-sweeper lock, or
// reports that another run holds it. The lock is session-level, so it lives on a
// connection taken out of the pool for the whole run and returned by release; if
// that connection dies the lock goes with it, which is the right failure — the
// next run takes it over rather than waiting on a lock nobody holds.
//
// release must be called exactly once when acquired is true. It unlocks on a
// context detached from the run's own, because the run's context is exactly
// what is cancelled when the worker shuts down mid-run, and the unlock is the
// one statement that must still happen then. If the unlock itself fails the
// connection is closed rather than returned to the pool: a pooled connection
// still holding the lock would make every later run report "another replica is
// sweeping" until it happened to be recycled.
func (c client) TryRetentionSweepLock(ctx context.Context) (release func() error, acquired bool, err error) {
	if c.pool == nil {
		return nil, false, errors.New("retention: no database pool")
	}
	conn, err := c.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("retention lock: acquire connection: %w", err)
	}
	ok, err := gen.New(conn).TryRetentionSweepLock(ctx)
	if err != nil || !ok {
		conn.Release()
		if err != nil {
			return nil, false, fmt.Errorf("retention lock: %w", err)
		}
		return nil, false, nil
	}
	release = func() error {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		released, uerr := gen.New(conn).ReleaseRetentionSweepLock(unlockCtx)
		if uerr == nil && !released {
			uerr = errors.New("the lock was not held by this session")
		}
		if uerr != nil {
			closeErr := conn.Conn().Close(unlockCtx)
			conn.Release()
			return errors.Join(fmt.Errorf("retention lock: release: %w", uerr), closeErr)
		}
		conn.Release()
		return nil
	}
	return release, true, nil
}
