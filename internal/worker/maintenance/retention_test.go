package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
	"github.com/inroad/inroad/internal/platform/queue"
)

// fakeTable scripts one table's batches: backlog rows wait to be deleted, at most
// Limit per call, and every call's request is recorded so a test can assert what
// the handler asked for (the window, the limit, the cursor it resumed from).
type fakeTable struct {
	backlog    int64
	dependents int64 // per deleted row
	err        error
	errAfter   int // fail on this call number (1-based); 0 = never
	calls      []coreapi.RetentionRequest
	onCall     func()
}

func (f *fakeTable) batch(_ context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	f.calls = append(f.calls, req)
	if f.onCall != nil {
		f.onCall()
	}
	if f.err != nil && (f.errAfter == 0 || len(f.calls) == f.errAfter) {
		return coreapi.RetentionBatch{}, f.err
	}
	n := min(f.backlog, int64(req.Limit))
	f.backlog -= n
	// The cursor encodes the call number, so a test can check the NEXT request
	// resumed from exactly the cursor THIS batch returned.
	next := coreapi.RetentionCursor{At: time.Unix(int64(len(f.calls)), 0).UTC(), ID: uuid.New()}
	return coreapi.RetentionBatch{Deleted: n, Dependents: n * f.dependents, Next: next}, nil
}

type fakeRetainer struct {
	deliverability, inbox, tracking, sends, deadLetters fakeTable
	order                                               []string
}

func (f *fakeRetainer) PurgeDeliverabilityEvents(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	f.order = append(f.order, "deliverability_events")
	return f.deliverability.batch(ctx, req)
}

func (f *fakeRetainer) PurgeInboxThreads(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	f.order = append(f.order, "inbox_threads")
	return f.inbox.batch(ctx, req)
}

func (f *fakeRetainer) RollupTrackingEvents(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	f.order = append(f.order, "tracking_events")
	return f.tracking.batch(ctx, req)
}

func (f *fakeRetainer) PurgeSends(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	f.order = append(f.order, "sends")
	return f.sends.batch(ctx, req)
}

func (f *fakeRetainer) PurgeDeadLetters(ctx context.Context, req coreapi.RetentionRequest) (coreapi.RetentionBatch, error) {
	f.order = append(f.order, "task_dead_letters")
	return f.deadLetters.batch(ctx, req)
}

var _ Retainer = (*fakeRetainer)(nil)

func allEnabled() RetentionPolicy {
	return RetentionPolicy{
		DeliverabilityEvents: 400 * day, InboxThreads: 401 * day, TrackingEvents: 402 * day,
		Sends: 403 * day, DeadLetters: 90 * day,
	}
}

func runRetention(t *testing.T, core Retainer, p RetentionPolicy, m *metrics.Metrics, opts RetentionOptions) error {
	t.Helper()
	return RetentionHandler(core, p, m, opts)(context.Background(), asynq.NewTask(queue.TaskMaintenanceRetention, nil))
}

// The default: every recipient-identifying table disabled. A disabled table must
// cost NOTHING — not one call. A handler that "checked" a disabled table with a
// zero window would be one refactor away from `now() - 0s`, i.e. every row.
func TestRetentionDisabledPolicyCallsNothing(t *testing.T) {
	core := &fakeRetainer{}
	if err := runRetention(t, core, RetentionPolicy{}, nil, RetentionOptions{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(core.order) != 0 {
		t.Fatalf("a fully disabled policy made %d calls (%v), want none", len(core.order), core.order)
	}
}

// Only the enabled table is swept, with exactly its own window — windows are
// per-table, so a handler that passed one table's window to another would be a
// silent policy violation.
func TestRetentionSweepsOnlyEnabledTablesWithTheirOwnWindow(t *testing.T) {
	core := &fakeRetainer{}
	p := RetentionPolicy{Sends: 500 * day, DeadLetters: 30 * day}
	if err := runRetention(t, core, p, nil, RetentionOptions{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	for name, tc := range map[string]struct {
		calls []coreapi.RetentionRequest
		want  time.Duration
	}{
		"deliverability": {core.deliverability.calls, 0},
		"inbox":          {core.inbox.calls, 0},
		"tracking":       {core.tracking.calls, 0},
		"sends":          {core.sends.calls, 500 * day},
		"dead letters":   {core.deadLetters.calls, 30 * day},
	} {
		if tc.want == 0 {
			if len(tc.calls) != 0 {
				t.Errorf("%s is disabled but was called %d times", name, len(tc.calls))
			}
			continue
		}
		if len(tc.calls) != 1 {
			t.Fatalf("%s: %d calls, want 1 (an empty backlog drains on the first batch)", name, len(tc.calls))
		}
		if tc.calls[0].OlderThan != tc.want {
			t.Errorf("%s window = %s, want %s", name, tc.calls[0].OlderThan, tc.want)
		}
	}
}

// Order is load-bearing (see retentionTables): the tables that hold a send back
// run before sends, and tracking rolls up before a send delete would cascade it.
func TestRetentionRunsTablesInDependencyOrder(t *testing.T) {
	core := &fakeRetainer{}
	if err := runRetention(t, core, allEnabled(), nil, RetentionOptions{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	want := []string{"deliverability_events", "inbox_threads", "tracking_events", "sends", "task_dead_letters"}
	if strings.Join(core.order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", core.order, want)
	}
}

// Batch boundaries. 12 rows at a batch of 5 is 5, 5, 2: the third batch is short,
// which is the drained signal, so there is no fourth call. Each batch after the
// first must resume from the cursor the one before it returned — resuming from
// the start would re-walk every row a guard kept, every batch.
func TestRetentionBatchesUntilAShortBatchAndResumesFromTheCursor(t *testing.T) {
	core := &fakeRetainer{sends: fakeTable{backlog: 12}}
	if err := runRetention(t, core, RetentionPolicy{Sends: 100 * day}, nil, RetentionOptions{BatchSize: 5}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	calls := core.sends.calls
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3 (5 + 5 + a short 2)", len(calls))
	}
	if !calls[0].After.At.IsZero() || calls[0].After.ID != uuid.Nil {
		t.Errorf("first batch cursor = %+v, want the zero cursor (before every row)", calls[0].After)
	}
	for i := 1; i < len(calls); i++ {
		if got, want := calls[i].After.At, time.Unix(int64(i), 0).UTC(); !got.Equal(want) {
			t.Errorf("batch %d resumed from %v, want the cursor batch %d returned (%v)", i+1, got, i, want)
		}
		if calls[i].Limit != 5 {
			t.Errorf("batch %d limit = %d, want 5", i+1, calls[i].Limit)
		}
	}
	if core.sends.backlog != 0 {
		t.Errorf("backlog left = %d, want 0", core.sends.backlog)
	}
}

// An exact multiple of the batch size needs one more, empty, batch to learn it
// is drained — a full batch never means "done".
func TestRetentionAFullLastBatchIsNotTakenAsDrained(t *testing.T) {
	core := &fakeRetainer{sends: fakeTable{backlog: 10}}
	if err := runRetention(t, core, RetentionPolicy{Sends: 100 * day}, nil, RetentionOptions{BatchSize: 5}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := len(core.sends.calls); got != 3 {
		t.Fatalf("calls = %d, want 3 (5, 5, then an empty batch that proves it drained)", got)
	}
}

// One table's backlog must not take the whole run: it stops at its batch cap,
// the tables after it still run, and the undrained table is reported at WARN.
func TestRetentionCapsOneTableSoTheRestStillRun(t *testing.T) {
	core := &fakeRetainer{deliverability: fakeTable{backlog: 1_000}, deadLetters: fakeTable{backlog: 1}}
	logs := captureLogs(t)
	err := runRetention(t, core, allEnabled(), nil, RetentionOptions{BatchSize: 10, MaxBatchesPerTable: 3})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if got := len(core.deliverability.calls); got != 3 {
		t.Errorf("deliverability calls = %d, want the cap of 3", got)
	}
	if len(core.deadLetters.calls) != 1 || core.deadLetters.backlog != 0 {
		t.Errorf("dead letters after a capped table: calls=%d backlog=%d, want it still swept", len(core.deadLetters.calls), core.deadLetters.backlog)
	}
	rec := findLog(t, logs, "retention backlog not drained this run; it resumes next run", "deliverability_events")
	if rec == nil {
		t.Fatal("an undrained table was not reported")
	}
	if rec["level"] != "WARN" || rec["rows"] != float64(30) || rec["drained"] != false {
		t.Errorf("undrained record = %v, want WARN rows=30 drained=false", rec)
	}
}

// The budget is checked between batches, on the injected clock. Once it is spent
// no new batch starts — in this table or any later one — and the run still ends
// cleanly: a spent budget is "resume next hour", not a failure to retry.
func TestRetentionStopsStartingBatchesOnceTheBudgetIsSpent(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	core := &fakeRetainer{inbox: fakeTable{backlog: 1_000}}
	// Every inbox batch advances the clock by a minute; the budget is 3 minutes.
	core.inbox.onCall = func() { now = now.Add(time.Minute) }

	err := runRetention(t, core, allEnabled(), nil, RetentionOptions{BatchSize: 10, Budget: 3 * time.Minute, Now: clock})
	if err != nil {
		t.Fatalf("a spent budget must not fail the run: %v", err)
	}
	if got := len(core.inbox.calls); got != 3 {
		t.Errorf("inbox calls = %d, want 3 (one per minute of a 3-minute budget)", got)
	}
	if len(core.tracking.calls)+len(core.sends.calls)+len(core.deadLetters.calls) != 0 {
		t.Error("a table after the budget was spent still started a batch")
	}
	if len(core.deliverability.calls) != 1 {
		t.Error("the table before the budget was spent should have run")
	}
}

// A failing table must not freeze every other table's retention, but the run
// must still fail so asynq retries it and the job ledger records why.
func TestRetentionOneFailingTableDoesNotStopTheOthers(t *testing.T) {
	boom := errors.New("statement timeout")
	core := &fakeRetainer{tracking: fakeTable{err: boom}, sends: fakeTable{backlog: 3}}
	err := runRetention(t, core, allEnabled(), nil, RetentionOptions{})
	if !errors.Is(err, boom) {
		t.Fatalf("handler error = %v, want it to carry the tracking failure", err)
	}
	if !strings.Contains(err.Error(), "tracking_events") {
		t.Errorf("error %q does not name the failing table", err)
	}
	if core.sends.backlog != 0 || len(core.deadLetters.calls) != 1 {
		t.Error("the tables after the failing one were not swept")
	}
}

// Rows an earlier batch deleted are gone even when a later batch fails, so the
// metric must count them — otherwise a failing run reads as one that did nothing.
func TestRetentionCountsRowsDeletedBeforeAFailure(t *testing.T) {
	boom := errors.New("connection reset")
	core := &fakeRetainer{sends: fakeTable{backlog: 100, dependents: 2, err: boom, errAfter: 3}}
	m := metrics.New()
	err := runRetention(t, core, RetentionPolicy{Sends: 100 * day}, m, RetentionOptions{BatchSize: 10})
	if !errors.Is(err, boom) {
		t.Fatalf("handler error = %v, want %v", err, boom)
	}
	families := metricstest.Scrape(t, m)
	if got := metricstest.CounterValue(families, "inroad_retention_rows_deleted_total",
		map[string]string{"table": "sends", "scope": "table"}); got != 20 {
		t.Errorf("sends rows counted = %v, want 20 (two successful batches before the failure)", got)
	}
	if got := metricstest.CounterValue(families, "inroad_retention_rows_deleted_total",
		map[string]string{"table": "sends", "scope": "dependent"}); got != 40 {
		t.Errorf("sends dependents counted = %v, want 40", got)
	}
}

// A cancelled context ends the run instead of trying every remaining table
// against a dead connection.
func TestRetentionStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	core := &fakeRetainer{}
	core.deliverability.onCall = cancel
	core.deliverability.err = context.Canceled
	err := RetentionHandler(core, allEnabled(), nil, RetentionOptions{})(ctx, asynq.NewTask(queue.TaskMaintenanceRetention, nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("handler error = %v, want context.Canceled", err)
	}
	if len(core.order) != 1 {
		t.Fatalf("calls after cancellation: %v, want only the first", core.order)
	}
}

// The log line is the per-run record an operator reads; it must carry the real
// numbers per table.
func TestRetentionLogsWhatEachTableDid(t *testing.T) {
	core := &fakeRetainer{inbox: fakeTable{backlog: 4, dependents: 3}}
	logs := captureLogs(t)
	if err := runRetention(t, core, RetentionPolicy{InboxThreads: 45 * day}, nil, RetentionOptions{}); err != nil {
		t.Fatalf("handler: %v", err)
	}
	rec := findLog(t, logs, "retention applied", "inbox_threads")
	if rec == nil {
		t.Fatal("no retention record for inbox_threads")
	}
	if rec["rows"] != float64(4) || rec["dependent_rows"] != float64(12) || rec["window_days"] != float64(45) || rec["drained"] != true {
		t.Errorf("record = %v, want rows=4 dependent_rows=12 window_days=45 drained=true", rec)
	}
}

func TestRetentionPolicyValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		policy  RetentionPolicy
		wantErr []string // substrings; empty means valid
	}{
		{name: "everything disabled is valid", policy: RetentionPolicy{}},
		{name: "every floor exactly is valid", policy: RetentionPolicy{
			DeliverabilityEvents: 90 * day, InboxThreads: 30 * day, TrackingEvents: 30 * day, Sends: 90 * day, DeadLetters: 7 * day,
		}},
		{name: "one day under each floor", policy: RetentionPolicy{
			DeliverabilityEvents: 89 * day, InboxThreads: 29 * day, TrackingEvents: 29 * day, Sends: 89 * day, DeadLetters: 6 * day,
		}, wantErr: []string{
			"INROAD_RETENTION_DELIVERABILITY_EVENTS_DAYS is 89 days, below the 90-day minimum",
			"INROAD_RETENTION_INBOX_DAYS is 29 days, below the 30-day minimum",
			"INROAD_RETENTION_TRACKING_EVENTS_DAYS is 29 days, below the 30-day minimum",
			"INROAD_RETENTION_SENDS_DAYS is 89 days, below the 90-day minimum",
			"INROAD_RETENTION_DEAD_LETTERS_DAYS is 6 days, below the 7-day minimum",
		}},
		{name: "negative is refused, not read as disabled", policy: RetentionPolicy{Sends: -day},
			wantErr: []string{"INROAD_RETENTION_SENDS_DAYS must not be negative"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.Validate()
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not report %q:\n%v", want, err)
				}
			}
		})
	}
}

func TestRetentionPolicyFromDaysMapsEachTable(t *testing.T) {
	p := RetentionPolicyFromDays(RetentionDays{DeliverabilityEvents: 1, InboxThreads: 2, TrackingEvents: 3, Sends: 4, DeadLetters: 5})
	want := RetentionPolicy{DeliverabilityEvents: day, InboxThreads: 2 * day, TrackingEvents: 3 * day, Sends: 4 * day, DeadLetters: 5 * day}
	if p != want {
		t.Fatalf("policy = %+v, want %+v", p, want)
	}
	if got := (RetentionPolicy{Sends: 400 * day}).Enabled(); len(got) != 1 || got["sends"] != 400 {
		t.Errorf("Enabled() = %v, want map[sends:400]", got)
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	restore := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })
	return &logs
}

// findLog returns the record with this message for this table, or nil.
func findLog(t *testing.T, logs *bytes.Buffer, msg, table string) map[string]any {
	t.Helper()
	for line := range bytes.SplitSeq(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line is not JSON (%s): %v", line, err)
		}
		if rec["msg"] == msg && rec["table"] == table {
			return rec
		}
	}
	return nil
}
