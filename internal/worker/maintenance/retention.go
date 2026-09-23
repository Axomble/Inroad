package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/metrics"
)

// Retainer is the narrow capability the retention sweep needs: one bounded batch
// per table. Separate from Cleaner because the two answer different questions —
// Cleaner purges things with a FIXED lifetime the code knows (a session expires,
// a replay-cache key is 24h), this purges records whose lifetime is the
// OPERATOR'S decision — and because each method here takes the window and the
// batch as arguments where Cleaner's take none.
//
// Satisfied by the in-process coreapi client only. The remote transport does not
// implement it, by design: every method is a cross-tenant DELETE (see
// coreapi/retention.go), so a fleet host can never be handed this sweep.
//
// What each batch keeps, and why, is documented on its SQL
// (internal/platform/db/queries/retention.sql): that is where the guards are, so
// that is where their reasons live.
type Retainer interface {
	// PurgeDeliverabilityEvents deletes provider bounce/complaint events that no
	// auto-pause rate can still read.
	PurgeDeliverabilityEvents(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error)
	// PurgeInboxThreads deletes whole idle conversations, messages and all.
	PurgeInboxThreads(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error)
	// RollupTrackingEvents folds raw open/click events into per-send rollups,
	// then deletes them, so reporting survives and the per-hit detail does not.
	RollupTrackingEvents(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error)
	// PurgeSends deletes campaign sends nothing live depends on.
	PurgeSends(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error)
	// PurgeDeadLetters deletes captured retry-exhausted tasks, every status.
	PurgeDeadLetters(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error)
}

// RetentionPolicy is how long each table keeps its rows. Zero DISABLES that
// table's sweep — it is never read as "keep nothing" (the in-process client
// refuses a non-positive window as a backstop, see encodeRetention).
//
// Built once, at the composition root, from INROAD_RETENTION_*_DAYS (see
// RetentionPolicyFromDays). The four recipient-identifying tables default to
// disabled: how long to keep data about the people a workspace emails is a
// Privacy/Legal decision, not a code default. Dead letters default to 90 days
// (config.DefaultRetentionDeadLettersDays).
type RetentionPolicy struct {
	DeliverabilityEvents time.Duration
	InboxThreads         time.Duration
	TrackingEvents       time.Duration
	Sends                time.Duration
	DeadLetters          time.Duration
}

const day = 24 * time.Hour

// retentionTable is one table's row in the sweep: what it is called, which
// setting feeds it, the shortest window it accepts and why, and its batch.
//
// ORDER IS LOAD-BEARING. A send is kept while a deliverability event names it or
// an inbox thread renders it, so those two tables run first and free their sends
// in the same run; tracking events run before sends so rows past the tracking
// window are rolled up before a send delete would cascade them away unrolled.
type retentionTable struct {
	name    string
	setting string
	floor   time.Duration
	why     string
	window  func(RetentionPolicy) time.Duration
	batch   func(Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error)
}

// retentionTables is the sweep, in the order it runs.
func retentionTables() []retentionTable {
	return []retentionTable{
		{
			name: "deliverability_events", setting: "INROAD_RETENTION_DELIVERABILITY_EVENTS_DAYS", floor: 90 * day,
			why: "the widest fixed rate window that reads these events is warmup health's 30 days (the breaker's own window is 7), " +
				"and the dedup key goes with a deleted row, so a provider replay must be long past; 90 days is 3x the widest window, " +
				"the same margin invariant 55 uses. A running campaign's unbounded fallback window is protected by a guard, not the floor",
			window: func(p RetentionPolicy) time.Duration { return p.DeliverabilityEvents },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.PurgeDeliverabilityEvents
			},
		},
		{
			name: "inbox_threads", setting: "INROAD_RETENTION_INBOX_DAYS", floor: 30 * day,
			why: "the inbox's widest time scope is \"this week\" and a daily send cap reads today's outbound messages; " +
				"30 days keeps a month of conversation an operator is plausibly still working",
			window: func(p RetentionPolicy) time.Duration { return p.InboxThreads },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.PurgeInboxThreads
			},
		},
		{
			name: "tracking_events", setting: "INROAD_RETENTION_TRACKING_EVENTS_DAYS", floor: 30 * day,
			why: "the only raw-row reader the rollup cannot serve is the bot classifier's burst rule, which looks back 10 minutes; " +
				"30 days leaves a month of per-hit detail for diagnosing a classification",
			window: func(p RetentionPolicy) time.Duration { return p.TrackingEvents },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.RollupTrackingEvents
			},
		},
		{
			name: "sends", setting: "INROAD_RETENTION_SENDS_DAYS", floor: 90 * day,
			why: "warmup health counts 30 days of sends as its denominator, a late bounce or reply is matched back through the send, " +
				"and a deleted send's tracked links stop redirecting; 90 days is 3x the widest window",
			window: func(p RetentionPolicy) time.Duration { return p.Sends },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.PurgeSends
			},
		},
		{
			name: "task_dead_letters", setting: "INROAD_RETENTION_DEAD_LETTERS_DAYS", floor: 7 * day,
			why:    "a dead letter is only useful while someone can still triage and replay it; a week covers a long weekend plus an outage",
			window: func(p RetentionPolicy) time.Duration { return p.DeadLetters },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.PurgeDeadLetters
			},
		},
	}
}

// RetentionDays is the operator's configuration in the unit it is stated in.
// Zero disables a table.
type RetentionDays struct {
	DeliverabilityEvents, InboxThreads, TrackingEvents, Sends, DeadLetters int
}

// RetentionPolicyFromDays converts whole days into the policy. A negative count
// becomes a negative window, which Validate refuses — it is not clamped to zero,
// because "disabled" must be something the operator wrote.
func RetentionPolicyFromDays(d RetentionDays) RetentionPolicy {
	return RetentionPolicy{
		DeliverabilityEvents: time.Duration(d.DeliverabilityEvents) * day,
		InboxThreads:         time.Duration(d.InboxThreads) * day,
		TrackingEvents:       time.Duration(d.TrackingEvents) * day,
		Sends:                time.Duration(d.Sends) * day,
		DeadLetters:          time.Duration(d.DeadLetters) * day,
	}
}

// Validate refuses a window shorter than its table's floor, naming the setting
// and the reason — every violation at once, so one restart fixes them all (the
// same rule config.Load follows). A floor is not a recommendation: it is the
// point below which a rate the product acts on, or a live path, would start
// reading a table the sweep had already emptied. Disabled (zero) is always valid;
// a negative window is not.
func (p RetentionPolicy) Validate() error {
	var errs []error
	for _, t := range retentionTables() {
		w := t.window(p)
		switch {
		case w == 0:
			continue
		case w < 0:
			errs = append(errs, fmt.Errorf("%s must not be negative (0 disables %s retention)", t.setting, t.name))
		case w < t.floor:
			errs = append(errs, fmt.Errorf("%s is %d days, below the %d-day minimum for %s: %s",
				t.setting, days(w), days(t.floor), t.name, t.why))
		}
	}
	return errors.Join(errs...)
}

// Enabled reports each table this policy sweeps, for the startup log line that
// answers "is retention on here, and for what" without reading the environment.
func (p RetentionPolicy) Enabled() map[string]int {
	out := map[string]int{}
	for _, t := range retentionTables() {
		if w := t.window(p); w > 0 {
			out[t.name] = days(w)
		}
	}
	return out
}

func days(d time.Duration) int { return int(d / day) }

// RetentionOptions bounds one run. The zero value takes the defaults below.
type RetentionOptions struct {
	// BatchSize is the row bound of one batch — one statement, one short
	// transaction.
	BatchSize int32
	// MaxBatchesPerTable caps one table's share of a run, so a backlog on one
	// table cannot hold the job for the whole budget and starve the tables after
	// it. A table left undrained is logged and resumes next run.
	MaxBatchesPerTable int
	// Budget is the wall time after which no NEW batch starts. It is a stop
	// signal between batches, not a timeout on one: a batch is one statement and
	// is never interrupted mid-flight except by the task's own context.
	Budget time.Duration
	// Now is the clock the budget is measured on; nil means time.Now.
	Now func() time.Time
}

const (
	// defaultRetentionBatchSize matches every other purge in this repo
	// (queries/maintenance.sql): 5000 rows is a sub-second delete on an indexed
	// age column, short enough that no batch's locks are noticeable.
	defaultRetentionBatchSize = 5000
	// defaultMaxBatchesPerTable: 40 × 5000 is 200k rows per table per run. The
	// sweep runs hourly, so a table drains at up to 4.8M rows a day — above the
	// write rate of any single-node deployment's busiest table — while one run
	// stays in the tens of seconds.
	defaultMaxBatchesPerTable = 40
	// defaultRetentionBudget is half of queue.sweepTimeout (10 minutes), the
	// ceiling asynq kills this handler at: a run that hits it stops cleanly and
	// is recorded as ok, rather than being killed mid-batch and retried.
	defaultRetentionBudget = 5 * time.Minute
)

func (o RetentionOptions) withDefaults() RetentionOptions {
	if o.BatchSize <= 0 {
		o.BatchSize = defaultRetentionBatchSize
	}
	if o.MaxBatchesPerTable <= 0 {
		o.MaxBatchesPerTable = defaultMaxBatchesPerTable
	}
	if o.Budget <= 0 {
		o.Budget = defaultRetentionBudget
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// RetentionHandler applies the policy to every enabled table, in order, in
// bounded batches.
//
// Idempotent and safe to run on several replicas at once: each batch deletes by
// age from the database clock and takes its rows with SKIP LOCKED, so an
// overlapping run takes a disjoint set rather than double-counting or waiting.
//
// A table that fails does not stop the tables after it — one table's error must
// not also freeze every other table's retention — but the run returns every
// failure joined, so asynq retries it and the ledger records the error. A retry
// is cheap: whatever the first attempt deleted is gone, and the rest resumes.
func RetentionHandler(core Retainer, policy RetentionPolicy, m *metrics.Metrics, opts RetentionOptions) func(context.Context, *asynq.Task) error {
	opts = opts.withDefaults()
	return func(ctx context.Context, _ *asynq.Task) error {
		deadline := opts.Now().Add(opts.Budget)
		var errs []error
		for _, t := range retentionTables() {
			window := t.window(policy)
			if window <= 0 {
				continue
			}
			if !opts.Now().Before(deadline) {
				slog.WarnContext(ctx, "retention run budget spent before this table; it resumes next run",
					"table", t.name, "budget", opts.Budget)
				continue
			}
			if err := applyRetention(ctx, t, t.batch(core), window, m, opts, deadline); err != nil {
				if ctx.Err() != nil {
					return errors.Join(append(errs, err)...)
				}
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

// applyRetention runs one table's batches until it drains, hits its batch cap,
// or the run's budget is spent — whichever comes first — and reports what it did.
func applyRetention(ctx context.Context, t retentionTable, batch func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error),
	window time.Duration, m *metrics.Metrics, opts RetentionOptions, deadline time.Time) error {
	var (
		total   coreapi.RetentionBatch
		cursor  coreapi.RetentionCursor
		batches int
		drained bool
		err     error
	)
	for batches < opts.MaxBatchesPerTable && opts.Now().Before(deadline) {
		var res coreapi.RetentionBatch
		res, err = batch(ctx, coreapi.RetentionRequest{OlderThan: window, Limit: opts.BatchSize, After: cursor})
		if err != nil {
			err = fmt.Errorf("retention %s: %w", t.name, err)
			break
		}
		batches++
		total.Deleted += res.Deleted
		total.Dependents += res.Dependents
		total.RolledUp += res.RolledUp
		if res.Deleted < int64(opts.BatchSize) {
			drained = true
			break
		}
		cursor = res.Next
	}
	// Recorded even when the loop ended on an error: rows earlier batches
	// deleted are gone, and a counter that under-reported them would make the
	// failing run look like it did nothing.
	m.RetentionRowsDeleted(t.name, total.Deleted, total.Dependents)
	attrs := []any{
		"table", t.name, "window_days", days(window), "rows", total.Deleted,
		"dependent_rows", total.Dependents, "batches", batches, "drained", drained,
	}
	if total.RolledUp > 0 {
		attrs = append(attrs, "rollup_rows", total.RolledUp)
	}
	switch {
	case err != nil:
		slog.ErrorContext(ctx, "retention batch failed", append(attrs, "err", err)...)
	case !drained:
		// Not an error: the backlog resumes next run. But a table that is never
		// drained is a window the sweep is not keeping, so it must be visible at
		// the default log level.
		slog.WarnContext(ctx, "retention backlog not drained this run; it resumes next run", attrs...)
	default:
		slog.InfoContext(ctx, "retention applied", attrs...)
	}
	return err
}
