package inprocess

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/fleetdecision"
)

// RecordWorkerProviderSignals persists one worker's accumulation window. This is
// the control-plane half of the per-worker signal path: the worker classifies at
// the capture point and accumulates in memory, then flushes here on a timer,
// because internal/worker reaches relational data only through coreapi.
//
// `worker_provider_signals` is global infrastructure state like `workers`, so
// there is no workspace pin — see the table's migration for why there is no
// tenant this row could honestly belong to.
func (c client) RecordWorkerProviderSignals(ctx context.Context, in coreapi.WorkerProviderSignals) error {
	if in.WorkerID == "" {
		return fmt.Errorf("coreapi: worker id required for provider signals")
	}
	// A quiet window is the normal state of a healthy worker, so an empty batch
	// is a no-op rather than an error — reporting it as one would make every idle
	// flush log a failure.
	if len(in.Counts) == 0 {
		return nil
	}
	if in.WindowEnd.Before(in.WindowStart) {
		return fmt.Errorf("coreapi: provider signal window ends (%s) before it starts (%s)", in.WindowEnd, in.WindowStart)
	}

	start := pgtype.Timestamptz{Time: in.WindowStart, Valid: true}
	end := pgtype.Timestamptz{Time: in.WindowEnd, Valid: true}
	rows := make([]gen.RecordWorkerProviderSignalsParams, 0, len(in.Counts))
	for _, count := range in.Counts {
		// A zero or negative delta violates the table's CHECK and would fail the
		// COPY for the WHOLE window, not just its own row. The collector cannot
		// produce one (a key exists only once it has been incremented), so this
		// is a backstop against a future caller rather than a live path — and
		// dropping the row is the right failure, since a counter that counted
		// nothing carries no information to lose.
		if count.Events <= 0 {
			continue
		}
		rows = append(rows, gen.RecordWorkerProviderSignalsParams{
			WorkerID:    in.WorkerID,
			Provider:    count.Provider,
			Operation:   count.Operation,
			Reason:      count.Reason,
			Events:      count.Events,
			WindowStart: start,
			WindowEnd:   end,
		})
	}
	if len(rows) == 0 {
		return nil
	}
	if _, err := c.q.RecordWorkerProviderSignals(ctx, rows); err != nil {
		return fmt.Errorf("coreapi: record worker provider signals: %w", err)
	}
	return nil
}

// RecordFleetDecision appends one automated fleet decision.
//
// Validation runs BEFORE the ids are parsed so a malformed entry reports what is
// actually wrong with it ("reason is empty") rather than an id parse failure on
// the field that happens to be read first.
func (c client) RecordFleetDecision(ctx context.Context, e fleetdecision.Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}

	params := gen.InsertFleetDecisionParams{
		Kind:        string(e.Kind),
		Reason:      e.Reason.String(),
		TriggeredBy: string(e.TriggeredBy),
	}
	if e.WorkerID != "" {
		// A refusal names no destination worker, and NULL is the honest
		// representation of that — an empty string would sort and group as if it
		// were a worker id.
		params.WorkerID = &e.WorkerID
	}
	// Entry.Validate has already established that these are both set or both
	// empty, so one parse guard covers the pair.
	if e.MailboxID != "" {
		mbID, err := uuid.Parse(e.MailboxID)
		if err != nil {
			// Both sentinels wrapped: ErrInvalidEntry so a caller can skip this
			// entry rather than retry it forever, and the parse error so the
			// message says what was actually malformed.
			return fmt.Errorf("%w: mailbox id: %w", fleetdecision.ErrInvalidEntry, err)
		}
		wsID, err := uuid.Parse(e.WorkspaceID)
		if err != nil {
			return fmt.Errorf("%w: workspace id: %w", fleetdecision.ErrInvalidEntry, err)
		}
		params.MailboxID = pgtype.UUID{Bytes: mbID, Valid: true}
		params.WorkspaceID = pgtype.UUID{Bytes: wsID, Valid: true}
	}

	if err := c.q.InsertFleetDecision(ctx, params); err != nil {
		return fmt.Errorf("coreapi: record fleet decision: %w", err)
	}
	return nil
}

// PurgeWorkerProviderSignals removes signal windows past their 30-day retention.
// Same reasoning as PurgeScheduledJobRuns and the purges around it (invariant
// 55): every live worker writes rows on a timer and nothing in the application
// ever deletes them, so the table needs a sweep from the day it exists rather
// than after it has grown.
func (c client) PurgeWorkerProviderSignals(ctx context.Context) (int64, error) {
	return c.q.PurgeWorkerProviderSignals(ctx)
}

// PurgeFleetDecisions removes decision-log rows past their 90-day retention.
// Wider than the signals' 30 days for the reason on the query: a decision
// records something that HAPPENED to a mailbox, and "when did this move, and
// why" outlives the counters that informed it.
func (c client) PurgeFleetDecisions(ctx context.Context) (int64, error) {
	return c.q.PurgeFleetDecisions(ctx)
}

var (
	_ coreapi.ProviderSignalClient  = client{}
	_ coreapi.FleetDecisionRecorder = client{}
)
