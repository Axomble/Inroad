//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// A migration that cannot be rolled back is only discovered during an incident.
// 000061 is the easy shape — new columns and a CHECK that had no predecessor — but
// "easy" is exactly the class of migration nobody exercises, and the thing it must
// prove is not that the DDL parses: it is that a rollback does not take reputation
// evidence with it.
//
// The observations the identities hang off are placement rows. Dropping five
// columns must leave every one of those rows, its placement, its attribution and
// its capability untouched, because a rollback of THIS migration is undoing a
// metadata addition and nothing else. Then forward again, which is what a
// rollback-then-redeploy actually does, and the CHECK must be creatable a second
// time — the case a down migration that only dropped the columns would still pass,
// and a down that forgot to make the constraint drop re-runnable would not.
//
// Runs on a database of its own (dbtest.ScratchDSN): the shared test database is
// migrated to the newest version by every other package, four of which run at once,
// so dropping a column there breaks whatever is mid-query.
func TestIdentityFactsMigrationRollsBackAndForwardAgain(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "identity_migration")

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close() // before the scratch DROP, which the t.Cleanup above runs after

	ws, mailbox := seedIdentityFixture(t, ctx, pool)
	insertIdentityObservation(t, ctx, pool, ws, mailbox, "ident-1",
		"acme.test", "bounce.acme.test", "pass", "pass", "fail")

	// The CHECK is live: a verdict outside the vocabulary is refused, which is what
	// makes normalising at the writer a requirement rather than a nicety.
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, idempotency_key, spf_result)
		 VALUES ($1, $2, 'placement', 'inbox', 'warmup_receipt', true, 'bad-verdict', 'softfail')`,
		ws, mailbox); err == nil {
		t.Fatal("'softfail' was accepted into spf_result: the vocabulary CHECK is not enforced, so a " +
			"later slice reading this column has to re-interpret raw receiver strings")
	}
	// And so is the empty string, which is why a zero-valued caller MUST be
	// normalised before it reaches here.
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, idempotency_key, dmarc_result)
		 VALUES ($1, $2, 'placement', 'inbox', 'warmup_receipt', true, 'empty-verdict', '')`,
		ws, mailbox); err == nil {
		t.Fatal("an empty dmarc_result was accepted: the CHECK is not enforced, and the writer's " +
			"normalisation would be untested")
	}

	// Down to 000060, i.e. undo exactly 000061. Named as a version and not as "one
	// step" so this keeps testing THIS migration after later ones land — the
	// fragility that broke 000060's rollback test when this migration was added.
	if err := db.MigrateTo(dsn, 60); err != nil {
		t.Fatalf("migrate down to 000060: %v", err)
	}

	// The evidence survives the rollback. Only the metadata went.
	var placement string
	var trusted, tabCapable bool
	if err := pool.QueryRow(ctx,
		`SELECT placement, attribution_trusted, tab_capable FROM warmup_observations
		  WHERE workspace_id = $1 AND idempotency_key = 'ident-1'`,
		ws).Scan(&placement, &trusted, &tabCapable); err != nil {
		t.Fatalf("the observation is gone after a rollback — undoing a metadata addition must not "+
			"destroy reputation evidence: %v", err)
	}
	if placement != "inbox" || !trusted {
		t.Errorf("observation came back as placement=%q trusted=%v, want inbox/true", placement, trusted)
	}
	for _, column := range []string{"dkim_domain", "return_path_domain", "spf_result", "dkim_result", "dmarc_result"} {
		if identityColumnExists(t, ctx, pool, column) {
			t.Errorf("after the rollback warmup_observations.%s still exists: the down migration is not symmetric", column)
		}
	}

	// Forward again on the SAME database. This is the half that catches a down
	// migration which left the constraint behind: ADD CONSTRAINT would then fail
	// with a duplicate name and the redeploy would not come up.
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	insertIdentityObservation(t, ctx, pool, ws, mailbox, "ident-2",
		"acme.test", "bounce.acme.test", "none", "neutral", "unknown")

	// The re-applied columns default correctly for the row that predates them, so a
	// pre-000061 observation states "nothing observed" rather than a verdict it never
	// earned.
	var spf, dkim, dmarc, dkimDomain string
	if err := pool.QueryRow(ctx,
		`SELECT spf_result, dkim_result, dmarc_result, dkim_domain FROM warmup_observations
		  WHERE workspace_id = $1 AND idempotency_key = 'ident-1'`,
		ws).Scan(&spf, &dkim, &dmarc, &dkimDomain); err != nil {
		t.Fatalf("read backfilled row: %v", err)
	}
	if spf != "unknown" || dkim != "unknown" || dmarc != "unknown" || dkimDomain != "" {
		t.Errorf("a row that predates the columns reads %q/%q/%q dkim_domain=%q, want all unknown and "+
			"an empty domain: absence of a verdict is not a verdict", spf, dkim, dmarc, dkimDomain)
	}
}

// seedIdentityFixture creates the workspace + mailbox the observations hang off.
// The tenant FK is composite (id, workspace_id), so a real pair is required.
func seedIdentityFixture(t *testing.T, ctx context.Context, pool gen.DBTX) (ws, mailbox uuid.UUID) {
	t.Helper()
	q := gen.New(pool)
	w, err := q.CreateWorkspace(ctx, "Identity migration "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "warm@ident.test", DisplayName: "Warm",
		SmtpHost: "smtp.ident.test", SmtpPort: 587, SmtpUsername: "warm@ident.test",
		ImapHost: "imap.ident.test", ImapPort: 993, ImapUsername: "warm@ident.test",
		SecretCiphertext: "ct", DailyCap: 50, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	return w.ID, mb.ID
}

func insertIdentityObservation(t *testing.T, ctx context.Context, pool gen.DBTX, ws, mailbox uuid.UUID,
	key, dkimDomain, returnPath, spf, dkim, dmarc string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, tab_capable, idempotency_key,
		                                  dkim_domain, return_path_domain, spf_result, dkim_result, dmarc_result)
		 VALUES ($1, $2, 'placement', 'inbox', 'warmup_receipt', true, false, $3, $4, $5, $6, $7, $8)`,
		ws, mailbox, key, dkimDomain, returnPath, spf, dkim, dmarc); err != nil {
		t.Fatalf("insert identity observation %s: %v", key, err)
	}
}

func identityColumnExists(t *testing.T, ctx context.Context, pool gen.DBTX, column string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name = 'warmup_observations' AND column_name = $1`, column).Scan(&n); err != nil {
		t.Fatalf("read columns: %v", err)
	}
	return n > 0
}
