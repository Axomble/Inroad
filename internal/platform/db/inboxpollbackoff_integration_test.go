//go:build integration

package db_test

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

// The migration under test, named as a version rather than "one step down" so
// this keeps testing IT after later migrations land.
const (
	beforePollBackoff = 20260923144934 // the version preceding it
	pollBackoffColumn = 20260924103858
)

// ladder is the production schedule (internal/worker/inbox.DefaultPollBackoff)
// expressed in the seconds the query takes. Duplicated here rather than
// imported because platform/* must never import worker/*; the unit test
// TestDefaultPollBackoffLadder pins the Go values these mirror.
var ladder = []float64{180, 360, 720, 1440, 2880, 3600}

// capRung is a one-rung ladder — how a caller says "this failure cannot fix
// itself, go straight to the cap".
var capRung = []float64{3600}

// backoffFixture is one workspace with one active mailbox, plus a second
// workspace used to prove the failure write is workspace-pinned.
type backoffFixture struct {
	ws, other uuid.UUID
	mailbox   uuid.UUID
}

func seedBackoffFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) backoffFixture {
	t.Helper()
	scalar := func(sql string, args ...any) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("seed (%s): %v", sql, err)
		}
		return id
	}
	ws := scalar(`INSERT INTO workspaces(name) VALUES($1) RETURNING id`, "Poll backoff "+uuid.NewString())
	other := scalar(`INSERT INTO workspaces(name) VALUES($1) RETURNING id`, "Poll backoff other "+uuid.NewString())
	return backoffFixture{
		ws:    ws,
		other: other,
		mailbox: scalar(`INSERT INTO mailboxes(workspace_id,email,secret_ciphertext)
		 VALUES($1,$2,'sealed') RETURNING id`, ws, "poller-"+uuid.NewString()+"@backoff.test"),
	}
}

// listed reports whether the poll fan-out would pick this mailbox up now.
func listed(t *testing.T, ctx context.Context, q *gen.Queries, id uuid.UUID) bool {
	t.Helper()
	rows, err := q.ListActiveMailboxes(ctx)
	if err != nil {
		t.Fatalf("ListActiveMailboxes: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TestPollBackoffWidensAndCaps walks the ladder the way an hour-long outage
// does: every consecutive failure must schedule the next attempt FURTHER out
// than the last, and the last rung must be a ceiling rather than a step.
//
// The assertion is on the DELTA the database computed (retry_after - now()),
// not on a time this process constructed, because the value is compared against
// the database clock and a few ms of host skew must not be able to decide it.
func TestPollBackoffWidensAndCaps(t *testing.T) {
	ctx := context.Background()
	pool, q := connectBackoff(t, ctx)
	fx := seedBackoffFixture(t, ctx, pool)

	var prev time.Duration
	for attempt, want := range []time.Duration{
		3 * time.Minute, 6 * time.Minute, 12 * time.Minute,
		24 * time.Minute, 48 * time.Minute, time.Hour, time.Hour, time.Hour,
	} {
		row, err := q.RecordInboxPollFailure(ctx, gen.RecordInboxPollFailureParams{
			BackoffSeconds: ladder, ID: fx.mailbox, WorkspaceID: fx.ws,
		})
		if err != nil {
			t.Fatalf("attempt %d: RecordInboxPollFailure: %v", attempt+1, err)
		}
		if int(row.InboxPollFailures) != attempt+1 {
			t.Fatalf("attempt %d recorded failure count %d", attempt+1, row.InboxPollFailures)
		}
		got := scheduledDelay(t, ctx, pool, fx.mailbox)
		if got < want-time.Minute || got > want+time.Minute {
			t.Errorf("failure %d scheduled the retry in %v, want ~%v", attempt+1, got, want)
		}
		// The property that actually matters, independent of the exact rungs:
		// it widens, and it never widens past the cap.
		if attempt > 0 && got < prev && want != time.Hour {
			t.Errorf("failure %d scheduled EARLIER (%v) than failure %d (%v)", attempt+1, got, attempt, prev)
		}
		if got > time.Hour+time.Minute {
			t.Errorf("failure %d scheduled %v out — past the cap; an outage would cost the connection", attempt+1, got)
		}
		prev = got
	}
}

// TestPollBackoffSuppressesSchedulingButNeverEligibility is the one this whole
// change exists to guarantee. A backed-off mailbox must vanish from the POLL
// fan-out and from nothing else: it is still 'active', a send still finds it,
// and it is polled again the moment its retry time passes.
//
// A backoff that quietly became a deactivation is the exact failure the task
// exists to prevent, so both halves are asserted, not just the suppression.
func TestPollBackoffSuppressesSchedulingButNeverEligibility(t *testing.T) {
	ctx := context.Background()
	pool, q := connectBackoff(t, ctx)
	fx := seedBackoffFixture(t, ctx, pool)

	if !listed(t, ctx, q, fx.mailbox) {
		t.Fatal("a fresh mailbox is not in the poll fan-out")
	}
	if _, err := q.RecordInboxPollFailure(ctx, gen.RecordInboxPollFailureParams{
		BackoffSeconds: ladder, ID: fx.mailbox, WorkspaceID: fx.ws,
	}); err != nil {
		t.Fatalf("RecordInboxPollFailure: %v", err)
	}

	if listed(t, ctx, q, fx.mailbox) {
		t.Error("a backed-off mailbox is still fanned out; the hammering is unchanged")
	}

	// Still ACTIVE, and therefore still sendable. MailboxExists is what the send
	// path gates on; writing status='error' here would flip it and cost the user
	// their mailbox.
	if ok, err := q.MailboxExists(ctx, fx.mailbox); err != nil || !ok {
		t.Errorf("MailboxExists = (%v, %v) for a backed-off mailbox; it must still SEND", ok, err)
	}
	if got := status(t, ctx, pool, fx.mailbox); got != "active" {
		t.Errorf("status = %q after a failed poll, want %q — the poller must never write status", got, "active")
	}
	if _, err := q.ReserveMailboxSendSlot(ctx, gen.ReserveMailboxSendSlotParams{
		ID: fx.mailbox, WorkspaceID: fx.ws,
	}); err != nil {
		t.Errorf("ReserveMailboxSendSlot on a backed-off mailbox: %v; it must still send", err)
	}

	// And it comes back on its own, with no operator action, once due.
	if _, err := pool.Exec(ctx,
		`UPDATE mailboxes SET inbox_poll_retry_after = now() - interval '1 second' WHERE id = $1`,
		fx.mailbox); err != nil {
		t.Fatalf("expire the backoff: %v", err)
	}
	if !listed(t, ctx, q, fx.mailbox) {
		t.Error("a mailbox past its retry time is still suppressed; the backoff became a deactivation")
	}
}

// TestSuccessfulPollClearsTheBackoff covers both cursor writes — the IMAP UID
// cursor and the opaque provider cursor — because a mailbox left parked on the
// cap after the server came back is the same outage, just slower.
func TestSuccessfulPollClearsTheBackoff(t *testing.T) {
	ctx := context.Background()
	pool, q := connectBackoff(t, ctx)

	for _, tc := range []struct {
		name  string
		clear func(t *testing.T, fx backoffFixture)
	}{
		{"SetInboxCursor (IMAP)", func(t *testing.T, fx backoffFixture) {
			if err := q.SetInboxCursor(ctx, gen.SetInboxCursorParams{
				ID: fx.mailbox, WorkspaceID: fx.ws, InboxLastSeenUid: 11, InboxUidValidity: 3,
			}); err != nil {
				t.Fatalf("SetInboxCursor: %v", err)
			}
		}},
		{"SetInboxCursorString (Gmail/Graph)", func(t *testing.T, fx backoffFixture) {
			if err := q.SetInboxCursorString(ctx, gen.SetInboxCursorStringParams{
				ID: fx.mailbox, WorkspaceID: fx.ws, InboxCursor: "history-42",
			}); err != nil {
				t.Fatalf("SetInboxCursorString: %v", err)
			}
		}},
		{"UpdateMailboxStatus (the operator's resume)", func(t *testing.T, fx backoffFixture) {
			if _, err := q.UpdateMailboxStatus(ctx, gen.UpdateMailboxStatusParams{
				ID: fx.mailbox, WorkspaceID: fx.ws, Status: "active",
			}); err != nil {
				t.Fatalf("UpdateMailboxStatus: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := seedBackoffFixture(t, ctx, pool)
			for range 4 {
				if _, err := q.RecordInboxPollFailure(ctx, gen.RecordInboxPollFailureParams{
					BackoffSeconds: ladder, ID: fx.mailbox, WorkspaceID: fx.ws,
				}); err != nil {
					t.Fatalf("RecordInboxPollFailure: %v", err)
				}
			}
			if listed(t, ctx, q, fx.mailbox) {
				t.Fatal("four failures did not suppress the fan-out")
			}

			tc.clear(t, fx)

			failures, retryAfter := backoffState(t, ctx, pool, fx.mailbox)
			if failures != 0 || retryAfter != nil {
				t.Errorf("after a success the backoff is (failures=%d, retry_after=%v), want (0, NULL)", failures, retryAfter)
			}
			if !listed(t, ctx, q, fx.mailbox) {
				t.Error("a recovered mailbox is still suppressed")
			}
		})
	}
}

// TestCapRungGoesStraightToTheCeiling is the AUTH shape: a one-rung ladder must
// schedule the cap on the very first failure, because a rejected sign-in cannot
// fix itself and repeating it is what gets an account locked.
func TestCapRungGoesStraightToTheCeiling(t *testing.T) {
	ctx := context.Background()
	pool, q := connectBackoff(t, ctx)
	fx := seedBackoffFixture(t, ctx, pool)

	if _, err := q.RecordInboxPollFailure(ctx, gen.RecordInboxPollFailureParams{
		BackoffSeconds: capRung, ID: fx.mailbox, WorkspaceID: fx.ws,
	}); err != nil {
		t.Fatalf("RecordInboxPollFailure: %v", err)
	}
	if got := scheduledDelay(t, ctx, pool, fx.mailbox); got < 59*time.Minute {
		t.Errorf("the first permanent failure scheduled a retry in %v, want the ~1h cap", got)
	}
}

// TestRecordInboxPollFailureIsWorkspacePinned: the mailbox UUID is unguessable,
// but the workspace pin is the invariant (docs/security.md 4), and a write that
// ignored it would let one tenant park another tenant's poller.
func TestRecordInboxPollFailureIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	pool, q := connectBackoff(t, ctx)
	fx := seedBackoffFixture(t, ctx, pool)

	_, err := q.RecordInboxPollFailure(ctx, gen.RecordInboxPollFailureParams{
		BackoffSeconds: ladder, ID: fx.mailbox, WorkspaceID: fx.other,
	})
	if err == nil {
		t.Fatal("a foreign workspace id recorded a failure; the query is not workspace-pinned")
	}
	if failures, retryAfter := backoffState(t, ctx, pool, fx.mailbox); failures != 0 || retryAfter != nil {
		t.Errorf("a foreign write moved the row to (failures=%d, retry_after=%v)", failures, retryAfter)
	}
}

// TestPollBackoffMigrationRollsBack walks the migration forwards, back and
// forwards again — what a rollback plus redeploy does. Rolling back must
// restore the previous fan-out exactly: every active mailbox eligible, no
// residue.
//
// Runs on a scratch database because it moves the schema backwards, which would
// break every package sharing the test database.
func TestPollBackoffMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "poll_backoff_migration")

	if err := db.MigrateTo(dsn, beforePollBackoff); err != nil {
		t.Fatalf("migrate to %d: %v", beforePollBackoff, err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close() // before the scratch DROP, which dbtest's t.Cleanup runs after

	fx := seedBackoffFixture(t, ctx, pool)
	q := gen.New(pool)

	if err := db.MigrateTo(dsn, pollBackoffColumn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	// An existing row must land eligible, not parked: the column defaults decide
	// whether an upgrade silently stops polling every mailbox in the install.
	if !listed(t, ctx, q, fx.mailbox) {
		t.Fatal("a pre-existing mailbox is not eligible after the migration; the upgrade stopped polling")
	}
	if _, err := q.RecordInboxPollFailure(ctx, gen.RecordInboxPollFailureParams{
		BackoffSeconds: ladder, ID: fx.mailbox, WorkspaceID: fx.ws,
	}); err != nil {
		t.Fatalf("RecordInboxPollFailure: %v", err)
	}
	if listed(t, ctx, q, fx.mailbox) {
		t.Fatal("the backoff does not suppress after the migration")
	}

	if err := db.MigrateTo(dsn, beforePollBackoff); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	var columns int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name = 'mailboxes'
		    AND column_name IN ('inbox_poll_failures','inbox_poll_retry_after')`).Scan(&columns); err != nil {
		t.Fatalf("column count: %v", err)
	}
	if columns != 0 {
		t.Errorf("%d backoff columns survived the down migration", columns)
	}

	if err := db.MigrateTo(dsn, pollBackoffColumn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	// Forward again: the mailbox that WAS backed off is eligible, because the
	// rollback dropped the state with the column.
	if !listed(t, ctx, q, fx.mailbox) {
		t.Error("after down+up the previously backed-off mailbox is still suppressed")
	}
}

func connectBackoff(t *testing.T, ctx context.Context) (*pgxpool.Pool, *gen.Queries) {
	t.Helper()
	dsn := dbtest.DSN(t)
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, gen.New(pool)
}

// scheduledDelay is how far out the DATABASE scheduled the next attempt,
// measured by the database, so host↔DB clock skew cannot decide the assertion.
func scheduledDelay(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) time.Duration {
	t.Helper()
	var secs float64
	if err := pool.QueryRow(ctx,
		`SELECT extract(epoch FROM inbox_poll_retry_after - now()) FROM mailboxes WHERE id = $1`,
		id).Scan(&secs); err != nil {
		t.Fatalf("scheduled delay: %v", err)
	}
	return time.Duration(secs * float64(time.Second))
}

func backoffState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (int, *time.Time) {
	t.Helper()
	var failures int
	var retryAfter *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT inbox_poll_failures, inbox_poll_retry_after FROM mailboxes WHERE id = $1`,
		id).Scan(&failures, &retryAfter); err != nil {
		t.Fatalf("backoff state: %v", err)
	}
	return failures, retryAfter
}

func status(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(ctx, `SELECT status FROM mailboxes WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("status: %v", err)
	}
	return s
}
