// Package jobrun records one row per periodic reconcile ("sweep") run, so
// "is the domain-auth sweep actually running" becomes a query over
// scheduled_job_runs instead of a log grep.
//
// internal/platform/metrics.SweepCompleted already exists, but only three of
// the six periodic reconciles in cmd/worker/scheduler.go's sweepRegistrars()
// call it (the enrollment, inbox and warmup scans), and even where it is
// called a Prometheus counter does not survive a scrape gap and carries no
// error message — exactly what an operator needs when a sweep silently stops
// running, or starts failing every tick. Record is a decorator that answers
// both gaps for all six, uniformly, WITHOUT changing any handler body.
//
// This package knows nothing about Postgres. Record takes a Recorder
// interface — the narrow, consumer-defined capability this package needs,
// satisfied by the coreapi in-process client via type assertion at the
// composition root, the same shape as worker/maintenance.Cleaner,
// worker/deliverability.Breaker and worker/recipientesp.Core. A coreapi
// client that does not implement it (a future HTTP coreapi that hasn't grown
// the endpoint yet, or a worker test fake) degrades to "records nothing",
// never to a failed sweep — see Record's own doc.
package jobrun

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/metrics"
)

// Outcome values a Run is recorded with. A recovered panic is recorded as
// OutcomeError (see Record) — there is no third "panicked" outcome, because a
// panic and a returned error mean the same thing to an operator reading this
// ledger: the run did not complete cleanly, and ErrorMessage says why.
const (
	OutcomeOK    = "ok"
	OutcomeError = "error"
)

// Name constants, one per periodic reconcile in cmd/worker/scheduler.go's
// sweepRegistrars(). Defined once, here, rather than as string literals at
// each of the two call sites that need to agree (sweepRegistrars() itself,
// and the Record wrapping in internal/worker/handlers.go) — so "the ledger's
// job names match the registrar names" holds by compilation, not by two
// people independently typing the same six strings correctly forever.
const (
	NameEnrollments        = "enrollments"
	NameInboxSweep         = "inbox sweep"
	NameWarmupSweep        = "warmup sweep"
	NameMaintenanceCleanup = "maintenance cleanup"
	NameDomainAuthSweep    = "domain auth sweep"
	NameRecipientESPSweep  = "recipient esp sweep"
)

// Recorder is the narrow coreapi capability Record needs: persist one
// completed run. Kept to this one method, like the capability interfaces it
// mirrors, so it is trivial to fake in a worker test and trivial for a future
// HTTP coreapi to grow.
type Recorder interface {
	RecordJobRun(ctx context.Context, run Run) error
}

// Run is one completed (or panicked) execution of a periodic reconcile.
type Run struct {
	Name       string
	StartedAt  time.Time
	FinishedAt time.Time
	Duration   time.Duration
	Outcome    string // OutcomeOK | OutcomeError
	// ErrorMessage is the failed run's error text, capped at
	// maxErrorMessageBytes — see capErrorMessage for what this can and cannot
	// promise about its contents.
	ErrorMessage string
}

// maxErrorMessageBytes caps what one run contributes to scheduled_job_runs.
// error_message, which is an unconstrained TEXT column in a table with no
// workspace_id and no per-tenant read path.
const maxErrorMessageBytes = 2 << 10 // 2 KiB

// truncatedErrorMarker tells an operator reading a ledger row that the text is
// not the whole error, so a truncated message is never mistaken for a complete
// one that simply ends oddly.
const truncatedErrorMarker = " …[truncated]"

// capErrorMessage bounds one run's stored error text.
//
// What this is NOT. The column's own comment used to say the value is "never
// tenant content", and this decorator cannot promise that: it stores
// err.Error() from six handlers it does not own, and any one of them wrapping a
// mailbox address, a recipient or a subject line would make the claim false the
// day it was written. What CAN be guaranteed is a bound, so that is what is
// guaranteed — an unbounded column reachable by arbitrary error text, in a table
// with no tenant scope, is a growth problem whichever way the content question
// is answered.
//
// The cut is on a RUNE boundary. A byte slice through the middle of a
// multi-byte rune is invalid UTF-8, which Postgres refuses outright (SQLSTATE
// 22021) — so a naive cap would turn a long error into a FAILED ledger write,
// which is strictly worse than the unbounded column it replaced. The START of
// the message is kept because that is where the cause is; the tail of a deeply
// wrapped error is the least diagnostic part of it.
func capErrorMessage(msg string) string {
	if len(msg) <= maxErrorMessageBytes {
		return msg
	}
	limit := maxErrorMessageBytes - len(truncatedErrorMarker)
	// range over a string yields RUNE START offsets, so end is the last one
	// that still fits inside the budget.
	end := 0
	for i := range msg {
		if i > limit {
			break
		}
		end = i
	}
	return msg[:end] + truncatedErrorMarker
}

// Record wraps handler so every invocation times the call, records exactly
// one scheduled_job_runs row through recorder, and emits the
// inroad_job_run_seconds Prometheus metric — all without changing what
// handler itself does.
//
// recorder may be nil: a coreapi client that does not implement the Recorder
// capability degrades to "records nothing, sweep still runs" — see the
// package doc. A non-nil recorder whose RecordJobRun call itself errors is
// handled the same way, just later: logged at warn and otherwise ignored.
// Observability must never break the thing it observes — a Postgres blip on
// the WRITE of a job-run row must not turn into a retried, or doubly run,
// sweep.
//
// mtx may be nil: every *metrics.Metrics method no-ops on a nil receiver
// (see platform/metrics' own doc), for the identical reason.
//
// A panic from handler is recovered, recorded as OutcomeError (message
// "panic: <value>"), and then RE-PANICKED. Swallowing it here would turn a
// crash into silence — strictly worse than the crash, because asynq's own
// retry logic (and, if retries are exhausted, the dead-letter capture path)
// depends on seeing the panic propagate; a decorator that ate it would make
// every wrapped sweep's crashes invisible everywhere except this ledger.
func Record(recorder Recorder, mtx *metrics.Metrics, name string, handler asynq.HandlerFunc) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) (err error) {
		started := time.Now()
		defer func() {
			if r := recover(); r != nil {
				observe(ctx, recorder, mtx, name, started, fmt.Errorf("panic: %v", r))
				panic(r)
			}
		}()
		err = handler(ctx, task)
		observe(ctx, recorder, mtx, name, started, err)
		return err
	}
}

// observe times, records and emits the metric for one completed run. Shared
// by Record's normal-return and panic-recovery paths so both build the same
// Run shape from the same outcome vocabulary — the alternative (duplicating
// this in the defer) is exactly the kind of divergence that would let one
// path's outcome spelling drift from the other's unnoticed.
func observe(ctx context.Context, recorder Recorder, mtx *metrics.Metrics, name string, started time.Time, runErr error) {
	finished := time.Now()
	duration := finished.Sub(started)

	outcome := OutcomeOK
	errMsg := ""
	if runErr != nil {
		outcome = OutcomeError
		errMsg = capErrorMessage(runErr.Error())
	}

	mtx.JobRunCompleted(name, outcome, duration)

	if recorder == nil {
		return
	}
	// Recording must never fail the wrapped handler (see Record's doc): the
	// result of this call is deliberately NOT returned to Record's caller,
	// only logged.
	if err := recorder.RecordJobRun(ctx, Run{
		Name:         name,
		StartedAt:    started,
		FinishedAt:   finished,
		Duration:     duration,
		Outcome:      outcome,
		ErrorMessage: errMsg,
	}); err != nil {
		slog.WarnContext(ctx, "jobrun_record_failed", "job", name, "err", err)
	}
}
