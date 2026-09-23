package inprocess

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// The recipient-data retention batches (maintenance.Retainer). Each method is one
// statement from queries/retention.sql or deadletter.sql — one short transaction
// — and every policy decision (what to keep, why) lives in that SQL, beside the
// guards it states. What is here is only the translation to and from the seam
// types, and it is written once (retentionBatch) so the five cannot disagree on
// how a window or a cursor is encoded, or on refusing a non-positive window.
//
// None of these is on the remote transport; see coreapi/retention.go.

// retentionParams is the parameter shape every retention query shares. sqlc
// generates one params struct per query, all with these four fields in this
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
// statement, wrap its error with what was being done, translate its row.
func retentionBatch[R any](ctx context.Context, req coreapi.RetentionRequest, what string,
	query func(context.Context, retentionParams) (R, error), result func(R) coreapi.RetentionBatch) (coreapi.RetentionBatch, error) {
	p, err := encodeRetention(req)
	if err != nil {
		return coreapi.RetentionBatch{}, err
	}
	row, err := query(ctx, p)
	if err != nil {
		return coreapi.RetentionBatch{}, fmt.Errorf("%s: %w", what, err)
	}
	return result(row), nil
}

// RollupTrackingEvents folds one batch of raw tracking events past the window
// into tracking_event_rollups and deletes them, in one statement.
func (c client) RollupTrackingEvents(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, req, "roll up tracking events",
		func(ctx context.Context, p retentionParams) (gen.RollupTrackingEventsRow, error) {
			return c.q.RollupTrackingEvents(ctx, gen.RollupTrackingEventsParams(p))
		},
		func(r gen.RollupTrackingEventsRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Deleted: r.DeletedRows, RolledUp: r.RollupRows, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// PurgeDeliverabilityEvents deletes one batch of bounce/complaint events past the
// window that no rate still reads.
func (c client) PurgeDeliverabilityEvents(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, req, "purge deliverability events",
		func(ctx context.Context, p retentionParams) (gen.PurgeDeliverabilityEventsRow, error) {
			return c.q.PurgeDeliverabilityEvents(ctx, gen.PurgeDeliverabilityEventsParams(p))
		},
		func(r gen.PurgeDeliverabilityEventsRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Deleted: r.DeletedRows, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// PurgeInboxThreads deletes one batch of whole inbox conversations with no
// activity inside the window, and their messages.
func (c client) PurgeInboxThreads(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, req, "purge inbox threads",
		func(ctx context.Context, p retentionParams) (gen.PurgeInboxThreadsRow, error) {
			return c.q.PurgeInboxThreads(ctx, gen.PurgeInboxThreadsParams(p))
		},
		func(r gen.PurgeInboxThreadsRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Deleted: r.DeletedRows, Dependents: r.DeletedMessages, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// PurgeSends deletes one batch of campaign sends past the window that nothing
// live depends on, with their tracking events and rollups.
func (c client) PurgeSends(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, req, "purge sends",
		func(ctx context.Context, p retentionParams) (gen.PurgeSendsRow, error) {
			return c.q.PurgeSends(ctx, gen.PurgeSendsParams(p))
		},
		func(r gen.PurgeSendsRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Deleted: r.DeletedRows, Dependents: r.DeletedTracking, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}

// PurgeDeadLetters deletes one batch of captured retry-exhausted tasks past the
// window. task_dead_letters is append-only in practice — triage flips a status,
// it never deletes — and had no sweep at all, so it grew forever on a system
// whose failure mode is a provider outage failing hundreds of queued sends at
// once (invariant 55's reasoning). It moved here from the daily maintenance
// cleanup, where its 90 days were hard-coded, so the window is the operator's
// (INROAD_RETENTION_DEAD_LETTERS_DAYS, default 90).
func (c client) PurgeDeadLetters(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	return retentionBatch(ctx, req, "purge dead letters",
		func(ctx context.Context, p retentionParams) (gen.PurgeTaskDeadLettersRow, error) {
			return c.q.PurgeTaskDeadLetters(ctx, gen.PurgeTaskDeadLettersParams(p))
		},
		func(r gen.PurgeTaskDeadLettersRow) coreapi.RetentionBatch {
			return coreapi.RetentionBatch{Deleted: r.DeletedRows, Next: decodeCursor(r.LastAt, r.LastID)}
		})
}
