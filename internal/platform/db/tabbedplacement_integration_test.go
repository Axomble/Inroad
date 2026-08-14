//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// A migration that cannot be rolled back is only discovered during an incident,
// and 000060 has the one shape that genuinely can fail: it WIDENS two CHECK
// constraints, so the rows it made legal are illegal again the moment the old
// constraint comes back. Rolling back with `tabbed` rows present is therefore not
// a formality — it is the case the down migration has to state an answer for.
//
// The answer this asserts: a tabbed row becomes `inbox`. It is what the row said
// before this migration existed (a Promotions landing was recorded as inbox), so
// rolling back restores exactly the pre-000060 state. The tab is lost; the
// placement, its daily projection and its sample are not. Deleting the rows would
// be the alternative and it is wrong — it destroys reputation evidence to undo a
// vocabulary change.
//
// It runs on a database of its own (dbtest.ScratchDSN): the shared test database
// is migrated to the newest version by every other package, four of which run at
// once, so dropping a column there breaks whatever is mid-query.
func TestTabbedPlacementMigrationRollsBackAndForwardAgain(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "tabbed_migration")

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close() // before the scratch DROP, which the t.Cleanup above runs after

	ws, mailbox := seedTabbedFixture(t, ctx, pool)

	// The widened vocabulary is live on BOTH tables. They have to agree: the
	// receipt is what the poller writes and the observation is what the policy
	// reads, in one transaction, so a value one accepts and the other rejects
	// aborts the receipt.
	receiptID := insertTabbedReceipt(t, ctx, pool, ws, mailbox)
	insertTabbedObservation(t, ctx, pool, ws, mailbox, "roundtrip-1")

	// A positively-identified tab implies a reader that could see labels, so
	// ('tabbed', tab_capable=false) is not a row that can honestly exist — and it is
	// worth refusing at the INSERT rather than absorbing later. Left representable,
	// it makes the tabbed rate's numerator able to exceed its denominator, which the
	// snapshot's own CHECK then catches by aborting the refresh for the whole
	// WORKSPACE: one mis-recorded row would stop promotions for every participant in
	// that tenant. (Observed for real while proving these tests fail on a revert.)
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, tab_capable, idempotency_key)
		 VALUES ($1, $2, 'placement', 'tabbed', 'warmup_receipt', true, false, 'incapable-tab')`,
		ws, mailbox); err == nil {
		t.Fatal("a tabbed placement was accepted from a reader that could not see tabs: " +
			"the numerator of the tabbed rate can now exceed its denominator")
	}

	if err := db.MigrateDown(dsn); err != nil {
		t.Fatalf("migrate down: %v", err)
	}

	// Rolled back: the evidence survives, demoted to what it said before tabs
	// existed, and the narrowed vocabulary is enforced again.
	assertPlacement(t, ctx, pool, `SELECT placement FROM warmup_receipts WHERE id = $1`, receiptID, "inbox")
	assertPlacement(t, ctx, pool,
		`SELECT placement FROM warmup_observations WHERE workspace_id = $1 AND kind = 'placement'`, ws, "inbox")
	if _, err := pool.Exec(ctx,
		`UPDATE warmup_receipts SET placement = 'tabbed' WHERE id = $1`, receiptID); err == nil {
		t.Fatal("after the rollback warmup_receipts still accepts 'tabbed': the old CHECK was not restored")
	}
	var tabCapableColumns int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name = 'warmup_observations' AND column_name = 'tab_capable'`).Scan(&tabCapableColumns); err != nil {
		t.Fatalf("read columns: %v", err)
	}
	if tabCapableColumns != 0 {
		t.Fatal("after the rollback warmup_observations.tab_capable still exists: the down migration is not symmetric")
	}

	// And forward again: the same database takes the migration a second time, which
	// is what a real rollback-then-redeploy does.
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE warmup_receipts SET placement = 'tabbed' WHERE id = $1`, receiptID); err != nil {
		t.Fatalf("after re-applying, 'tabbed' is rejected again: %v", err)
	}
	insertTabbedObservation(t, ctx, pool, ws, mailbox, "roundtrip-2")
}

// seedTabbedFixture creates the workspace + mailbox the placement rows hang off.
// Both tables' tenant FKs are composite (id, workspace_id), so a real pair is
// required — there is no shortcut that skips the mailbox.
func seedTabbedFixture(t *testing.T, ctx context.Context, pool gen.DBTX) (ws, mailbox uuid.UUID) {
	t.Helper()
	q := gen.New(pool)
	w, err := q.CreateWorkspace(ctx, "Tabbed migration "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "warm@tab.test", DisplayName: "Warm",
		SmtpHost: "smtp.tab.test", SmtpPort: 587, SmtpUsername: "warm@tab.test",
		ImapHost: "imap.tab.test", ImapPort: 993, ImapUsername: "warm@tab.test",
		SecretCiphertext: "ct", DailyCap: 50, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	return w.ID, mb.ID
}

// insertTabbedReceipt writes a receipt with no send anchor: warmup_send_id is
// nullable, and this test is about the placement CHECK, not the send path.
func insertTabbedReceipt(t *testing.T, ctx context.Context, pool gen.DBTX, ws, mailbox uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO warmup_receipts (workspace_id, recipient_mailbox, placement, source_folder)
		 VALUES ($1, $2, 'tabbed', 'INBOX') RETURNING id`, ws, mailbox).Scan(&id); err != nil {
		t.Fatalf("insert tabbed receipt: %v", err)
	}
	return id
}

func insertTabbedObservation(t *testing.T, ctx context.Context, pool gen.DBTX, ws, mailbox uuid.UUID, key string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, tab_capable, idempotency_key)
		 VALUES ($1, $2, 'placement', 'tabbed', 'warmup_receipt', true, true, $3)`,
		ws, mailbox, key); err != nil {
		t.Fatalf("insert tabbed observation: %v", err)
	}
}

func assertPlacement(t *testing.T, ctx context.Context, pool gen.DBTX, query string, arg any, want string) {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx, query, arg).Scan(&got); err != nil {
		if err == pgx.ErrNoRows {
			t.Fatalf("%s: the row is gone — a rollback must not destroy reputation evidence", query)
		}
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Fatalf("placement = %q, want %q", got, want)
	}
}
