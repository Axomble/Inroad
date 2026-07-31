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
}

// CleanupHandler purges expired security artifacts. Returning database errors
// lets asynq retry; successful runs log the affected count for observability.
func CleanupHandler(core Cleaner) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		deleted, err := core.CleanupExpired(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired security artifacts purged", "rows", deleted)
		return nil
	}
}
