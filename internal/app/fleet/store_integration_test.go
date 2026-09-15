//go:build integration

package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These exercise PgStore against real Postgres, because the unit tests inject a
// fake Store and therefore cannot say anything at all about the SQL — and the
// SQL is where the two properties that matter live: the workspace pin, and the
// arithmetic that turns per-verdict window deltas into the pairs an operator
// reads. Docker must be up.

func setup(t *testing.T) (*pgxpool.Pool, *gen.Queries, *PgStore) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	return pool, q, NewPgStore(q)
}

func mintWorkspace(t *testing.T, q *gen.Queries, label string) uuid.UUID {
	t.Helper()
	ws, err := q.CreateWorkspace(context.Background(), label+" "+uuid.NewString())
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return ws.ID
}

// mintMailbox creates a real mailbox row. mailbox_worker_assignments FKs to it
// and fleet_decisions FKs to the (id, workspace_id) pair, so an invented UUID
// would fail the inserts these tests depend on.
func mintMailbox(t *testing.T, q *gen.Queries, ws uuid.UUID) uuid.UUID {
	t.Helper()
	mb, err := q.CreateMailbox(context.Background(), gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: "mb-" + uuid.NewString() + "@x.test", DisplayName: "MB",
		SmtpHost: "smtp.x.test", SmtpPort: 587, SmtpUsername: "u",
		ImapHost: "imap.x.test", ImapPort: 993, ImapUsername: "u",
		SecretCiphertext: "ciphertext", DailyCap: 100, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	return mb.ID
}

// heartbeat registers a worker with a last_seen_at `age` in the past. Aged
// rather than deleted, because a stale row is the strictly harder case: it still
// joins and must be excluded on the timestamp alone.
func heartbeat(t *testing.T, pool *pgxpool.Pool, workerID, egressIP string, age time.Duration) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO workers (worker_id, egress_ip, id_family, last_seen_at)
		 VALUES ($1, $2, 'ipv4', now() - $3::interval)
		 ON CONFLICT (worker_id) DO UPDATE
		 SET egress_ip = EXCLUDED.egress_ip, last_seen_at = EXCLUDED.last_seen_at`,
		workerID, egressIP, age.String())
	if err != nil {
		t.Fatalf("heartbeat %s: %v", workerID, err)
	}
}

func pin(t *testing.T, pool *pgxpool.Pool, mailbox, ws uuid.UUID, workerID, band string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO mailbox_worker_assignments (mailbox_id, workspace_id, worker_id, band)
		 VALUES ($1, $2, $3, $4)`, mailbox, ws, workerID, band)
	if err != nil {
		t.Fatalf("pin mailbox: %v", err)
	}
}

func signal(t *testing.T, pool *pgxpool.Pool, workerID, provider, operation, reason string, events int64, age time.Duration) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO worker_provider_signals
		     (worker_id, provider, operation, reason, events, window_start, window_end)
		 VALUES ($1, $2, $3, $4, $5, now() - $6::interval - interval '5 minutes', now() - $6::interval)`,
		workerID, provider, operation, reason, events, age.String())
	if err != nil {
		t.Fatalf("insert signal: %v", err)
	}
}

// THE TENANCY TEST. Two workspaces, each with a mailbox on a worker of its own
// plus one worker they share. Neither may see the other's worker, and the shared
// worker's mailbox COUNT must be each caller's own footprint rather than the
// fleet-wide occupancy.
//
// Remove `AND a.workspace_id = @workspace_id::uuid` from the JOIN in
// ListWorkspaceFleetWorkers and this fails: every assertion below is about rows
// that exist and belong to somebody else, which is exactly the shape a
// single-workspace fixture is structurally blind to.
func TestWorkerListIsPinnedToTheCallersWorkspace(t *testing.T) {
	ctx := context.Background()
	pool, q, store := setup(t)

	mine := mintWorkspace(t, q, "fleet mine")
	theirs := mintWorkspace(t, q, "fleet theirs")

	suffix := uuid.NewString()
	myWorker := "w-mine-" + suffix
	theirWorker := "w-theirs-" + suffix
	shared := "w-shared-" + suffix

	heartbeat(t, pool, myWorker, "203.0.113.1", time.Minute)
	heartbeat(t, pool, theirWorker, "203.0.113.2", time.Minute)
	heartbeat(t, pool, shared, "203.0.113.3", time.Minute)

	pin(t, pool, mintMailbox(t, q, mine), mine, myWorker, "healthy")
	pin(t, pool, mintMailbox(t, q, mine), mine, shared, "degraded")
	// The other tenant puts THREE mailboxes on the shared worker, so a count
	// that forgot its filter would read 4 instead of 1.
	pin(t, pool, mintMailbox(t, q, theirs), theirs, theirWorker, "healthy")
	for range 3 {
		pin(t, pool, mintMailbox(t, q, theirs), theirs, shared, "healthy")
	}

	got, err := store.Workers(ctx, mine)
	if err != nil {
		t.Fatalf("Workers: %v", err)
	}

	seen := map[string]gen.ListWorkspaceFleetWorkersRow{}
	for _, row := range got {
		seen[row.WorkerID] = row
	}
	if _, leaked := seen[theirWorker]; leaked {
		t.Errorf("read worker %q, which carries only another tenant's mailboxes — the workspace "+
			"pin on the assignment join is missing", theirWorker)
	}
	if _, ok := seen[myWorker]; !ok {
		t.Errorf("did not read %q, which carries this workspace's own mailbox", myWorker)
	}

	sharedRow, ok := seen[shared]
	if !ok {
		t.Fatalf("did not read the shared worker %q", shared)
	}
	if sharedRow.WorkspaceMailboxes != 1 {
		t.Errorf("the shared worker reports %d of this workspace's mailboxes, want 1. Four mailboxes "+
			"sit on it in total, so a count of 4 means the FILTER counted another tenant's pins",
			sharedRow.WorkspaceMailboxes)
	}
	if sharedRow.DegradedMailboxes != 1 {
		t.Errorf("degraded count = %d, want 1", sharedRow.DegradedMailboxes)
	}
	if sharedRow.EgressIp != "203.0.113.3" {
		t.Errorf("egress_ip = %q, want 203.0.113.3", sharedRow.EgressIp)
	}

	// And the mirror image: the other tenant sees its own worker and not mine.
	theirView, err := store.Workers(ctx, theirs)
	if err != nil {
		t.Fatalf("Workers(theirs): %v", err)
	}
	for _, row := range theirView {
		if row.WorkerID == myWorker {
			t.Errorf("the other workspace read %q, which carries only my mailbox", myWorker)
		}
		if row.WorkerID == shared && row.WorkspaceMailboxes != 3 {
			t.Errorf("the other workspace counts %d mailboxes on the shared worker, want 3",
				row.WorkspaceMailboxes)
		}
	}
}

// A worker that stopped heartbeating must STILL be listed — that is the row an
// operator needs most — with the stale timestamp the service then reads as dead.
// Dropping it would make a silently-stopped worker look like a mailbox that was
// never placed.
func TestADeadWorkerIsStillListedWithItsStaleHeartbeat(t *testing.T) {
	ctx := context.Background()
	pool, q, store := setup(t)

	ws := mintWorkspace(t, q, "fleet dead")
	dead := "w-dead-" + uuid.NewString()
	heartbeat(t, pool, dead, "203.0.113.9", 3*time.Hour)
	pin(t, pool, mintMailbox(t, q, ws), ws, dead, "healthy")

	got, err := store.Workers(ctx, ws)
	if err != nil {
		t.Fatalf("Workers: %v", err)
	}
	if len(got) != 1 || got[0].WorkerID != dead {
		t.Fatalf("got %+v, want the dead worker listed", got)
	}
	if age := time.Since(got[0].LastSeenAt.Time); age < 2*time.Hour {
		t.Errorf("last_seen_at is %s old, want ~3h — the stale heartbeat is what the liveness "+
			"verdict is computed from", age)
	}
}

// THE ROLLUP ARITHMETIC. Each reason must land in the bucket the operator reads
// it from, and `attempts` must be the total across every reason INCLUDING the
// unclassified 'other' — so the named buckets can never silently fail to account
// for the whole.
func TestProviderSignalRollupBucketsEachVerdictAndTotalsEverything(t *testing.T) {
	ctx := context.Background()
	pool, q, store := setup(t)

	ws := mintWorkspace(t, q, "fleet rollup")
	worker := "w-rollup-" + uuid.NewString()
	heartbeat(t, pool, worker, "203.0.113.4", time.Minute)
	pin(t, pool, mintMailbox(t, q, ws), ws, worker, "healthy")

	// One of every reason on the same (provider, operation) leg, each with a
	// distinct count so a value landing in the wrong column is visible.
	signal(t, pool, worker, "gmail", "send", "ok", 100, time.Minute)
	signal(t, pool, worker, "gmail", "send", "auth_failed", 7, time.Minute)
	signal(t, pool, worker, "gmail", "send", "rate_limited", 3, time.Minute)
	signal(t, pool, worker, "gmail", "send", "throttled", 5, time.Minute)
	signal(t, pool, worker, "gmail", "send", "blocked", 11, time.Minute)
	signal(t, pool, worker, "gmail", "send", "unreachable", 2, time.Minute)
	signal(t, pool, worker, "gmail", "send", "rejected", 13, time.Minute)
	signal(t, pool, worker, "gmail", "send", "other", 17, time.Minute)
	// A different leg, so the grouping is proven to split rather than pool.
	signal(t, pool, worker, "smtp", "poll", "ok", 4, time.Minute)

	rows, err := store.ProviderSignals(ctx, ws, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ProviderSignals: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rollup rows, want 2 — one per (provider, operation): %+v", len(rows), rows)
	}

	// Ordered by (worker, provider, operation): gmail sorts before smtp.
	gmail := rows[0]
	if gmail.Provider != "gmail" || gmail.Operation != "send" {
		t.Fatalf("first row is %s/%s, want gmail/send", gmail.Provider, gmail.Operation)
	}
	for _, tc := range []struct {
		field string
		got   int64
		want  int64
	}{
		{"attempts", gmail.Attempts, 158}, // every reason, 'other' included
		{"successes", gmail.Successes, 100},
		{"auth_failures", gmail.AuthFailures, 7},
		{"throttled", gmail.Throttled, 8}, // rate_limited + throttled
		{"blocked", gmail.Blocked, 13},    // blocked + unreachable
		{"rejected", gmail.Rejected, 13},  // recipient-side, its own bucket
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.field, tc.got, tc.want)
		}
	}
	// The unclassified remainder must be recoverable, which is the whole reason
	// attempts is a total rather than a sum of the named buckets.
	classified := gmail.Successes + gmail.AuthFailures + gmail.Throttled + gmail.Blocked + gmail.Rejected
	if gmail.Attempts-classified != 17 {
		t.Errorf("attempts - classified = %d, want the 17 'other' events; attempts is a total so "+
			"nothing can go unaccounted for", gmail.Attempts-classified)
	}

	if rows[1].Provider != "smtp" || rows[1].Attempts != 4 {
		t.Errorf("second row = %+v, want the smtp/poll leg with 4 attempts", rows[1])
	}
}

// Signals belonging to a worker this workspace has NOTHING on must not be
// returned. Remove the `worker_id IN (SELECT ... WHERE workspace_id = ...)`
// subquery and this fails — the rollup would hand a tenant the provider verdicts
// of every worker in the deployment.
func TestProviderSignalRollupIsScopedToTheCallersOwnWorkers(t *testing.T) {
	ctx := context.Background()
	pool, q, store := setup(t)

	mine := mintWorkspace(t, q, "fleet sig mine")
	theirs := mintWorkspace(t, q, "fleet sig theirs")

	suffix := uuid.NewString()
	myWorker, theirWorker := "w-sig-mine-"+suffix, "w-sig-theirs-"+suffix
	heartbeat(t, pool, myWorker, "203.0.113.5", time.Minute)
	heartbeat(t, pool, theirWorker, "203.0.113.6", time.Minute)
	pin(t, pool, mintMailbox(t, q, mine), mine, myWorker, "healthy")
	pin(t, pool, mintMailbox(t, q, theirs), theirs, theirWorker, "healthy")

	signal(t, pool, myWorker, "smtp", "send", "ok", 1, time.Minute)
	signal(t, pool, theirWorker, "smtp", "send", "auth_failed", 999, time.Minute)

	rows, err := store.ProviderSignals(ctx, mine, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ProviderSignals: %v", err)
	}
	for _, row := range rows {
		if row.WorkerID == theirWorker {
			t.Errorf("read the provider verdicts of %q, a worker carrying none of this workspace's "+
				"mailboxes — the worker-set subquery's workspace pin is missing", theirWorker)
		}
	}
	if len(rows) != 1 || rows[0].WorkerID != myWorker {
		t.Errorf("got %+v, want exactly this workspace's own worker", rows)
	}
}

// The window is a filter on window_end, and a signal older than it must drop
// out. Without this, `since` could be ignored entirely and every other rollup
// assertion would still pass.
func TestProviderSignalRollupHonoursTheWindow(t *testing.T) {
	ctx := context.Background()
	pool, q, store := setup(t)

	ws := mintWorkspace(t, q, "fleet window")
	worker := "w-window-" + uuid.NewString()
	heartbeat(t, pool, worker, "203.0.113.8", time.Minute)
	pin(t, pool, mintMailbox(t, q, ws), ws, worker, "healthy")

	signal(t, pool, worker, "smtp", "send", "ok", 5, time.Minute)
	signal(t, pool, worker, "smtp", "send", "auth_failed", 50, 48*time.Hour)

	recent, err := store.ProviderSignals(ctx, ws, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ProviderSignals(1h): %v", err)
	}
	if len(recent) != 1 || recent[0].Attempts != 5 || recent[0].AuthFailures != 0 {
		t.Errorf("one-hour window = %+v, want only the 5 recent successes — the 48-hour-old auth "+
			"failures are outside it", recent)
	}

	wide, err := store.ProviderSignals(ctx, ws, time.Now().Add(-72*time.Hour))
	if err != nil {
		t.Fatalf("ProviderSignals(72h): %v", err)
	}
	if len(wide) != 1 || wide[0].Attempts != 55 || wide[0].AuthFailures != 50 {
		t.Errorf("72-hour window = %+v, want both windows summed (55 attempts, 50 auth failures)", wide)
	}
}

// The decision log's read path, through the store this domain added as its
// caller. A foreign workspace asking about the same mailbox id gets nothing.
func TestDecisionsForMailboxArePinnedToTheCallersWorkspace(t *testing.T) {
	ctx := context.Background()
	pool, q, store := setup(t)

	mine := mintWorkspace(t, q, "fleet dec mine")
	theirs := mintWorkspace(t, q, "fleet dec theirs")
	mailbox := mintMailbox(t, q, mine)

	worker := "w-dec-" + uuid.NewString()
	reason := "chose " + worker + " (score 0.82) over w-other (score 0.31), 4 candidates considered"
	for _, d := range []struct{ kind, reason string }{
		{"assign", reason},
		{"rotate", "forced: the incumbent stopped heartbeating"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO fleet_decisions (kind, worker_id, mailbox_id, workspace_id, reason, triggered_by)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			d.kind, worker, mailbox, mine, d.reason, "auto:"+d.kind); err != nil {
			t.Fatalf("insert decision: %v", err)
		}
	}

	got, err := store.DecisionsForMailbox(ctx, mine, mailbox, 10)
	if err != nil {
		t.Fatalf("DecisionsForMailbox: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d decisions, want 2", len(got))
	}
	// Newest first, and the prose survives byte for byte — a decision's reason is
	// what an operator acts on.
	if got[0].Kind != "rotate" {
		t.Errorf("first decision is %q, want the newest (rotate)", got[0].Kind)
	}
	if got[1].Reason != reason {
		t.Errorf("reason = %q, want it stored and returned verbatim: %q", got[1].Reason, reason)
	}

	foreign, err := store.DecisionsForMailbox(ctx, theirs, mailbox, 10)
	if err != nil {
		t.Fatalf("DecisionsForMailbox(theirs): %v", err)
	}
	if len(foreign) != 0 {
		t.Errorf("a foreign workspace read %d decisions about another tenant's mailbox, want 0",
			len(foreign))
	}
}

// The scheduled-job read this branch adds: one row per job, the LATEST run
// whether or not it falls inside the window, and failure counts that do.
func TestScheduledJobHealthReportsTheLatestRunPerJobAndWindowedFailures(t *testing.T) {
	ctx := context.Background()
	pool, _, store := setup(t)

	// The ledger has no tenant scope, so a shared test database means other
	// packages' rows may be present. Every assertion is therefore keyed to
	// job names unique to this run.
	suffix := uuid.NewString()
	healthy := "sweep-healthy-" + suffix
	failing := "sweep-failing-" + suffix
	stopped := "sweep-stopped-" + suffix

	run := func(job string, age time.Duration, outcome string, durationMs int64) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`INSERT INTO scheduled_job_runs (job_name, started_at, finished_at, duration_ms, outcome, error_message)
			 VALUES ($1, now() - $2::interval, now() - $2::interval + interval '1 second', $3, $4, $5)`,
			job, age.String(), durationMs, outcome,
			// Deliberately tenant-shaped text, to prove the read path cannot
			// surface it no matter what the writer stored.
			"dial tcp: smtp.acme-customer.test: connection refused"); err != nil {
			t.Fatalf("insert run: %v", err)
		}
	}

	run(healthy, 10*time.Minute, "ok", 1200)
	run(healthy, 5*time.Minute, "ok", 1300)
	run(failing, 30*time.Minute, "error", 400)
	run(failing, 10*time.Minute, "ok", 500)
	run(failing, 2*time.Minute, "error", 600)
	// This one last ran outside the window entirely — the sweep that silently
	// stopped, which is the single most important row this ledger can show.
	run(stopped, 96*time.Hour, "ok", 900)

	rows, err := store.ScheduledJobs(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("ScheduledJobs: %v", err)
	}
	byName := map[string]gen.ListScheduledJobHealthRow{}
	for _, row := range rows {
		byName[row.JobName] = row
	}

	if got := byName[healthy]; got.LastOutcome != "ok" || got.RunsInWindow != 2 || got.FailuresInWindow != 0 {
		t.Errorf("healthy job = %+v, want ok / 2 runs / 0 failures", got)
	} else if got.LastFailureAt.Valid {
		t.Errorf("healthy job reports a last failure at %s, want NULL", got.LastFailureAt.Time)
	} else if got.LastDurationMs != 1300 {
		t.Errorf("healthy job duration = %d, want the LATEST run's 1300", got.LastDurationMs)
	}

	bad := byName[failing]
	if bad.LastOutcome != "error" || bad.RunsInWindow != 3 || bad.FailuresInWindow != 2 {
		t.Errorf("failing job = %+v, want error / 3 runs / 2 failures", bad)
	}
	if !bad.LastFailureAt.Valid {
		t.Error("failing job reports no last failure timestamp")
	} else if age := time.Since(bad.LastFailureAt.Time); age > 5*time.Minute {
		t.Errorf("last_failure_at is %s old, want the most recent failure (~2m), not the oldest", age)
	}

	// The whole point: a job whose last run predates the window is still
	// reported, with that old timestamp, so "this sweep stopped four days ago"
	// is visible instead of the job vanishing from the list.
	gone, ok := byName[stopped]
	if !ok {
		t.Fatal("a job whose last run is outside the window vanished from the list — that is the " +
			"exact failure this ledger exists to make visible")
	}
	if gone.RunsInWindow != 0 {
		t.Errorf("stopped job reports %d runs in the window, want 0", gone.RunsInWindow)
	}
	if age := time.Since(gone.LastStartedAt.Time); age < 90*time.Hour {
		t.Errorf("stopped job's last run is %s old, want ~96h", age)
	}
}
