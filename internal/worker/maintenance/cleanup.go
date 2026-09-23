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
	// PurgeDeadWorkers reaps worker-registry rows long past the assigner's live
	// window plus the mailbox assignments pinned to them. Separate again: this is
	// global infrastructure state, not a security artifact or an HTTP concern.
	PurgeDeadWorkers(ctx context.Context) (deleted int64, err error)
	// PurgeDeadLetters removes captured retry-exhausted tasks past their 90-day
	// retention. Separate for the same reason as the two above: a dead letter is
	// a record of dropped work, neither a security artifact nor an HTTP concern.
	// It is here at all because the table had no sweep and grows with failures
	// nobody schedules — the reasoning behind invariant 55's warmup purge.
	PurgeDeadLetters(ctx context.Context) (deleted int64, err error)
	// PurgeWebhookDeliveries removes webhook_deliveries rows past their 30-day
	// retention. Same reasoning as PurgeDeadLetters: append-only in practice,
	// grows one row per (event, endpoint), and had no sweep of its own.
	PurgeWebhookDeliveries(ctx context.Context) (deleted int64, err error)
	// PurgeScheduledJobRuns removes scheduled_job_runs rows past their 30-day
	// retention. Same reasoning as PurgeDeadLetters/PurgeWebhookDeliveries:
	// append-only from internal/platform/jobrun.Record, eight jobs writing a row
	// per run (several every five minutes), and no sweep of its own until this
	// one — see the table's migration for why it needed one from day one rather
	// than growing unbounded first.
	PurgeScheduledJobRuns(ctx context.Context) (deleted int64, err error)
	// PurgeWorkerProviderSignals removes per-worker provider signal windows past
	// their 30-day retention. Same reasoning as PurgeScheduledJobRuns: every live
	// worker writes rows on a timer (one flush every five minutes) and nothing in
	// the application deletes them, so the table needed a sweep from the day it
	// existed rather than after it had grown. 30 days rather than 90 because a
	// window delta answers "how is this egress IP being treated right now".
	PurgeWorkerProviderSignals(ctx context.Context) (deleted int64, err error)
	// PurgeFleetDecisions removes fleet decision-log rows past their 90-day
	// retention. Wider than the signals above because a decision records
	// something that HAPPENED to a mailbox, and "when did this move, and why"
	// outlives the counters that informed it.
	PurgeFleetDecisions(ctx context.Context) (deleted int64, err error)
}

// AuditPurger is the audit-log retention capability. Separate from Cleaner,
// and consumed only when retention is configured, because it is the one purge
// here with NO default window: how long a security log is kept is a
// Privacy/Legal decision (INROAD_AUDIT_RETENTION_DAYS, unset = keep forever),
// so a deployment that has not made it must never delete an audit row.
type AuditPurger interface {
	PurgeAuditEvents(ctx context.Context, retentionDays int) (deleted int64, err error)
}

// CleanupOption configures an optional purge.
type CleanupOption func(*cleanupConfig)

type cleanupConfig struct {
	audit           AuditPurger
	auditRetainDays int
}

// WithAuditRetention adds the audit-log purge, deleting rows older than
// retentionDays. retentionDays <= 0 (retention disabled) or a nil purger adds
// nothing, so the composition root can pass the configured value through
// unconditionally.
func WithAuditRetention(p AuditPurger, retentionDays int) CleanupOption {
	return func(c *cleanupConfig) {
		if p == nil || retentionDays <= 0 {
			return
		}
		c.audit, c.auditRetainDays = p, retentionDays
	}
}

// CleanupHandler purges, in order: expired security artifacts, expired
// Idempotency-Key replay-cache rows, warmup evidence past its retention window,
// dead workers with their mailbox assignments, captured dead letters past
// theirs, expired webhook deliveries, scheduled-job-run ledger rows past theirs,
// per-worker provider signal windows, fleet decision-log rows, and — only when
// WithAuditRetention configured it — audit events past their retention.
// Returning a database error from any purge lets asynq retry; successful runs
// log each affected count for observability.
func CleanupHandler(core Cleaner, opts ...CleanupOption) func(context.Context, *asynq.Task) error {
	var cfg cleanupConfig
	for _, opt := range opts {
		opt(&cfg)
	}
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

		workersDeleted, err := core.PurgeDeadWorkers(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "dead workers and their assignments purged", "rows", workersDeleted)

		deadLettersDeleted, err := core.PurgeDeadLetters(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired dead letters purged", "rows", deadLettersDeleted)

		webhookDeliveriesDeleted, err := core.PurgeWebhookDeliveries(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired webhook deliveries purged", "rows", webhookDeliveriesDeleted)

		jobRunsDeleted, err := core.PurgeScheduledJobRuns(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired scheduled job runs purged", "rows", jobRunsDeleted)

		signalsDeleted, err := core.PurgeWorkerProviderSignals(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired worker provider signals purged", "rows", signalsDeleted)

		decisionsDeleted, err := core.PurgeFleetDecisions(ctx)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "expired fleet decisions purged", "rows", decisionsDeleted)

		if cfg.audit == nil {
			return nil
		}
		auditDeleted, err := cfg.audit.PurgeAuditEvents(ctx, cfg.auditRetainDays)
		if err != nil {
			return err
		}
		slog.InfoContext(ctx, "audit events past retention purged", "rows", auditDeleted, "retention_days", cfg.auditRetainDays)
		return nil
	}
}
