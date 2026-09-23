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

	// LoadRetentionProgress and SaveRetentionProgress persist each table's
	// cursor across runs. A run that restarted from the oldest row every hour
	// would spend its whole batch budget re-reading rows a guard keeps (the
	// sends of long-lived active enrollments) and never reach the deletable
	// rows behind them.
	LoadRetentionProgress(ctx context.Context, table string) (coreapi.RetentionProgress, error)
	SaveRetentionProgress(ctx context.Context, table string, cursor coreapi.RetentionCursor, newCycle bool) error

	// TryRetentionSweepLock makes the sweep single-instance deployment-wide.
	// asynq elects no scheduler leader, so replicas can overlap a run; row-level
	// SKIP LOCKED keeps that safe for rows but not across tables — the tracking
	// rollup and the sends purge lock the same rows in opposite orders — and a
	// shared cursor only means anything with one writer. release must be called
	// once when acquired is true.
	TryRetentionSweepLock(ctx context.Context) (release func() error, acquired bool, err error)
}

// RetentionPolicy is how long each table keeps its rows. Zero DISABLES that
// table's sweep — it is never read as "keep nothing" (the in-process client
// refuses a non-positive window as a backstop, see encodeRetention).
//
// Built once, at the composition root, from INROAD_RETENTION_*_DAYS (see
// RetentionPolicyFromDays). The four recipient-data windows (sends; inbox
// threads with their messages; tracking events; deliverability events) default
// to disabled: how long to keep data about the people a workspace emails is a
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
	// scan is the rows one batch examines. Larger for the guarded tables, where
	// most scanned rows may be kept and cost only an index probe each; smaller
	// where every scanned row is deleted (and, for tracking, rolled up).
	scan   int32
	window func(RetentionPolicy) time.Duration
	batch  func(Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error)
}

const (
	// guardedScan is the candidate bound for the tables whose rows a guard may
	// keep. 20000 index-range rows plus a handful of index probes each is well
	// inside the per-batch statement timeout (inprocess.retentionStatementTimeout).
	guardedScan = 20000
	// unguardedScan matches every other purge in this repo (queries/maintenance.sql):
	// every row scanned is deleted, and 5000 deletes is a sub-second statement.
	unguardedScan = 5000
)

// retentionTables is the sweep, in the order it runs.
func retentionTables() []retentionTable {
	return []retentionTable{
		{
			name: "deliverability_events", setting: "INROAD_RETENTION_DELIVERABILITY_EVENTS_DAYS", floor: 90 * day, scan: guardedScan,
			why: "the widest fixed rate window that reads these events is warmup health's 30 days (the breaker's own window is 7), " +
				"and the dedup key goes with a deleted row, so a provider replay must be long past; 90 days is 3x the widest window, " +
				"the same margin invariant 55 uses. A running campaign's unbounded fallback window is protected by a guard, not the floor",
			window: func(p RetentionPolicy) time.Duration { return p.DeliverabilityEvents },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.PurgeDeliverabilityEvents
			},
		},
		{
			name: "inbox_threads", setting: "INROAD_RETENTION_INBOX_DAYS", floor: 30 * day, scan: guardedScan,
			why: "the inbox's widest time scope is \"this week\" and a daily send cap reads today's outbound messages; " +
				"30 days keeps a month of conversation an operator is plausibly still working",
			window: func(p RetentionPolicy) time.Duration { return p.InboxThreads },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.PurgeInboxThreads
			},
		},
		{
			name: "tracking_events", setting: "INROAD_RETENTION_TRACKING_EVENTS_DAYS", floor: 30 * day, scan: unguardedScan,
			why: "every reader of tracking events must read tracking_engagement, which includes rolled-up rows, and the few that read " +
				"the raw table are pinned by trackingreaders_test.go (the bot classifier's 10-minute burst rule is the only one on a " +
				"live decision path); 30 days leaves a month of per-hit detail for diagnosing a classification",
			window: func(p RetentionPolicy) time.Duration { return p.TrackingEvents },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.RollupTrackingEvents
			},
		},
		{
			name: "sends", setting: "INROAD_RETENTION_SENDS_DAYS", floor: 90 * day, scan: guardedScan,
			why: "warmup health counts 30 days of sends as its denominator, a late bounce or reply is matched back through the send, " +
				"and a deleted send's tracked links stop redirecting; 90 days is 3x the widest window",
			window: func(p RetentionPolicy) time.Duration { return p.Sends },
			batch: func(r Retainer) func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
				return r.PurgeSends
			},
		},
		{
			name: "task_dead_letters", setting: "INROAD_RETENTION_DEAD_LETTERS_DAYS", floor: 7 * day, scan: unguardedScan,
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
	// BatchSize, when set, overrides every table's scan bound (retentionTable.scan).
	// Tests use it to cross batch boundaries with a handful of rows.
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
	// defaultMaxBatchesPerTable: 40 batches is 200k rows per unguarded table per
	// run and 800k scanned per guarded one. The sweep runs hourly, so a table
	// drains at up to 4.8M deletes a day — above the write rate of any
	// single-node deployment's busiest table — while one run stays in the tens of
	// seconds.
	defaultMaxBatchesPerTable = 40
	// defaultRetentionBudget is half of queue.sweepTimeout (10 minutes), the
	// ceiling asynq kills this handler at: a run that hits it stops cleanly and
	// is recorded as ok, rather than being killed mid-batch and retried.
	defaultRetentionBudget = 5 * time.Minute
)

func (o RetentionOptions) withDefaults() RetentionOptions {
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
// bounded batches, resuming each table from where the previous run stopped.
//
// Single-instance deployment-wide (TryRetentionSweepLock): a run that finds
// another in progress does nothing and succeeds, because the other one is doing
// the work. Every batch is still idempotent on its own — deletes by age on the
// database clock, rows taken with SKIP LOCKED — so a run killed mid-way leaves
// nothing half-done.
//
// A table that fails does not stop the tables after it — one table's error must
// not also freeze every other table's retention — but the run returns every
// failure joined, so asynq retries it and the ledger records the error. A retry
// is cheap: whatever the first attempt deleted is gone, and the rest resumes
// from the saved cursor.
func RetentionHandler(core Retainer, policy RetentionPolicy, m *metrics.Metrics, opts RetentionOptions) func(context.Context, *asynq.Task) error {
	opts = opts.withDefaults()
	return func(ctx context.Context, _ *asynq.Task) (runErr error) {
		if len(policy.Enabled()) == 0 {
			return nil
		}
		release, acquired, err := core.TryRetentionSweepLock(ctx)
		if err != nil {
			return err
		}
		if !acquired {
			slog.InfoContext(ctx, "retention sweep already running elsewhere; this run does nothing")
			return nil
		}
		defer func() {
			if err := release(); err != nil {
				runErr = errors.Join(runErr, err)
			}
		}()

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
			if err := applyRetention(ctx, core, t, window, m, opts, deadline); err != nil {
				if ctx.Err() != nil {
					return errors.Join(append(errs, err)...)
				}
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

// applyRetention runs one table's batches from its saved cursor until it drains,
// hits its batch cap, or the run's budget is spent — whichever comes first —
// saves where it stopped, and reports what it did.
//
// The cursor starts over from the oldest row when the table drains (so the next
// run re-checks rows a guard kept), and when a day has passed since it last
// started over (so a table too big to drain in one run still gets its kept rows
// re-checked daily rather than never).
func applyRetention(ctx context.Context, core Retainer, t retentionTable, window time.Duration,
	m *metrics.Metrics, opts RetentionOptions, deadline time.Time) error {
	scan := t.scan
	if opts.BatchSize > 0 {
		scan = opts.BatchSize
	}
	progress, err := core.LoadRetentionProgress(ctx, t.name)
	if err != nil {
		return fmt.Errorf("retention %s: %w", t.name, err)
	}
	cursor, newCycle := progress.Cursor, false
	if progress.CycleExpired {
		cursor, newCycle = coreapi.RetentionCursor{}, true
	}
	resumed := cursor != (coreapi.RetentionCursor{})

	batch := t.batch(core)
	var (
		total   coreapi.RetentionBatch
		batches int
		drained bool
	)
	for batches < opts.MaxBatchesPerTable && opts.Now().Before(deadline) {
		var res coreapi.RetentionBatch
		res, err = batch(ctx, coreapi.RetentionRequest{OlderThan: window, Limit: scan, After: cursor})
		if err != nil {
			err = fmt.Errorf("retention %s: %w", t.name, err)
			break
		}
		batches++
		total.Scanned += res.Scanned
		total.Deleted += res.Deleted
		total.Dependents += res.Dependents
		total.RolledUp += res.RolledUp
		if res.Scanned < int64(scan) {
			drained = true
			break
		}
		cursor = res.Next
	}
	// Saved even when the loop ended on an error: the cursor only ever advances
	// past a batch that committed, so resuming from it loses nothing.
	save := cursor
	if drained {
		save, newCycle = coreapi.RetentionCursor{}, true
	}
	if serr := core.SaveRetentionProgress(ctx, t.name, save, newCycle); serr != nil {
		err = errors.Join(err, fmt.Errorf("retention %s: %w", t.name, serr))
	}

	// Recorded even when the loop ended on an error: rows earlier batches
	// deleted are gone, and a counter that under-reported them would make the
	// failing run look like it did nothing.
	m.RetentionRowsDeleted(t.name, total.Deleted, total.Dependents)
	attrs := []any{
		"table", t.name, "window_days", days(window), "scanned", total.Scanned, "rows", total.Deleted,
		"dependent_rows", total.Dependents, "batches", batches, "drained", drained, "resumed", resumed,
	}
	if total.RolledUp > 0 {
		attrs = append(attrs, "rollup_rows", total.RolledUp)
	}
	switch {
	case err != nil:
		slog.ErrorContext(ctx, "retention batch failed", append(attrs, "err", err)...)
	case !drained:
		// Not an error: the backlog resumes next run from the saved cursor. But a
		// table that is never drained is a window the sweep is not keeping, so it
		// must be visible at the default log level.
		slog.WarnContext(ctx, "retention backlog not drained this run; it resumes next run", attrs...)
	default:
		slog.InfoContext(ctx, "retention applied", attrs...)
	}
	return err
}
