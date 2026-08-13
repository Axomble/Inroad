// Package maintenance owns low-frequency storage lifecycle jobs.
package maintenance

import (
	"context"
	"log/slog"

	"github.com/hibiken/asynq"
)

// Cleaner is the narrow capability this job needs. Keeping it separate from
// coreapi.Client avoids coupling every send-path test double to maintenance.
type Cleaner interface {
	CleanupExpired(ctx context.Context) (deleted int64, err error)
	// PurgeIdempotencyKeys removes Idempotency-Key replay-cache rows past
	// their fixed 24h retention window. Kept as its own method rather than
	// folded into CleanupExpired: that method's own doc scopes it to
	// authentication/authorization artifacts specifically, and the
	// idempotency cache is an HTTP-layer concern, not a security artifact.
	PurgeIdempotencyKeys(ctx context.Context) (deleted int64, err error)
	// PurgeWarmupObservations removes warmup evidence past its 90-day retention.
	// Separate for the same reason: it is neither a security artifact nor an
	// HTTP-layer concern, and its retention is driven by the widest window any
	// reputation query reads (30 days) plus margin.
	PurgeWarmupObservations(ctx context.Context) (deleted int64, err error)
}

// CleanupHandler purges expired security artifacts, expired Idempotency-Key
// replay-cache rows, AND warmup evidence past its retention window. Returning a database error from either
// purge lets asynq retry; successful runs log each affected count for
// observability.
func CleanupHandler(core Cleaner) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		deleted, err := core.CleanupExpired(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired security artifacts purged", "rows", deleted)

		idempotencyDeleted, err := core.PurgeIdempotencyKeys(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired idempotency keys purged", "rows", idempotencyDeleted)

		observationsDeleted, err := core.PurgeWarmupObservations(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired warmup observations purged", "rows", observationsDeleted)
		return nil
	}
}
