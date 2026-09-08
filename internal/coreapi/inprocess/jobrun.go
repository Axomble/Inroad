package inprocess

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/jobrun"
)

// RecordJobRun implements jobrun.Recorder: persist one completed periodic
// reconcile run. Called by internal/platform/jobrun.Record, wrapped around
// each of the six sweeps in internal/worker/handlers.go — never by an HTTP
// caller, so there is no workspace to pin (scheduled_job_runs carries none;
// see its migration's own doc for why these rows are instance-scoped).
func (c client) RecordJobRun(ctx context.Context, run jobrun.Run) error {
	return c.q.InsertScheduledJobRun(ctx, gen.InsertScheduledJobRunParams{
		JobName:      run.Name,
		StartedAt:    pgtype.Timestamptz{Time: run.StartedAt, Valid: true},
		FinishedAt:   pgtype.Timestamptz{Time: run.FinishedAt, Valid: true},
		DurationMs:   run.Duration.Milliseconds(),
		Outcome:      run.Outcome,
		ErrorMessage: run.ErrorMessage,
	})
}

// The BINDING half of the type-assertion pattern, as in the five siblings that
// carry one. internal/worker/handlers.go resolves this capability with
// `core.(jobrun.Recorder)` and treats a miss as "record nothing, run the sweep
// anyway", so a signature drift here would make all six periodic reconciles stop
// writing to the ledger with nothing failing anywhere.
var _ jobrun.Recorder = client{}
