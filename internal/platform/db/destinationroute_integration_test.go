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

// 000062 adds ONE column and one CHECK, which is the shape of migration nobody
// exercises — and the shape whose rollback is discovered to be broken during an
// incident. What it has to prove is not that the DDL parses: it is that undoing
// the route recording does not take the reputation evidence the routes hang off,
// and that the vocabulary is closed by the database rather than only by the Go
// seam above it.
//
// Anchored to a VERSION (61) rather than "one step down". A step count silently
// retargets whichever migration is newest, so a test written as `-1` stops testing
// this migration the moment 000063 lands — the fragility that broke 000060's
// rollback test when 000061 was added.
//
// Runs on a database of its own (dbtest.ScratchDSN): the shared test database is
// migrated to the newest version by every other package, four of which run at
// once, so dropping a column there breaks whatever is mid-query.
func TestDestinationRouteMigrationRollsBackAndForwardAgain(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "destination_route_migration")

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close() // before the scratch DROP, which ScratchDSN's cleanup runs after

	ws, mailbox := seedRouteFixture(t, ctx, pool)
	insertRouteObservation(t, ctx, pool, ws, mailbox, "route-google", "google")

	// The CHECK is live, and it closes the vocabulary to esp.ESP's four values. A
	// fifth spelling of "Google" reaching this column would split one route into two
	// in every matrix that groups by it.
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, idempotency_key, destination_esp)
		 VALUES ($1, $2, 'placement', 'inbox', 'warmup_receipt', true, 'bad-route', 'gmail')`,
		ws, mailbox); err == nil {
		t.Fatal("'gmail' was accepted into destination_esp: the vocabulary CHECK is not enforced, so the " +
			"route matrix would group the same destination under two names")
	}
	// The empty string too, which is why a caller that predates this column MUST be
	// normalised (or rely on the DEFAULT) rather than sending a zero value.
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, idempotency_key, destination_esp)
		 VALUES ($1, $2, 'placement', 'inbox', 'warmup_receipt', true, 'empty-route', '')`,
		ws, mailbox); err == nil {
		t.Fatal("an empty destination_esp was accepted: the CHECK is not enforced, and the writer's " +
			"normalisation would be untested")
	}

	// A row that says nothing about its destination defaults to 'unknown' — never to
	// a route it was not measured on.
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, idempotency_key)
		 VALUES ($1, $2, 'placement', 'inbox', 'warmup_receipt', true, 'route-default')`,
		ws, mailbox); err != nil {
		t.Fatalf("insert a row without a destination: %v", err)
	}
	if got := readDestination(t, ctx, pool, ws, "route-default"); got != "unknown" {
		t.Errorf("a row written without a destination reads %q, want \"unknown\": defaulting into a real "+
			"route would claim a measurement that never happened", got)
	}

	// Down to 000061, i.e. undo exactly 000062.
	if err := db.MigrateTo(dsn, 61); err != nil {
		t.Fatalf("migrate down to 000061: %v", err)
	}

	// The evidence survives. A rollback of this migration undoes a route RECORDING
	// and nothing else: the placement, its attribution and its capability are what
	// the policy reads, and none of them are this migration's to destroy.
	var placement string
	var trusted, tabCapable bool
	if err := pool.QueryRow(ctx,
		`SELECT placement, attribution_trusted, tab_capable FROM warmup_observations
		  WHERE workspace_id = $1 AND idempotency_key = 'route-google'`,
		ws).Scan(&placement, &trusted, &tabCapable); err != nil {
		t.Fatalf("the observation is gone after a rollback — undoing a route recording must not destroy "+
			"reputation evidence: %v", err)
	}
	if placement != "inbox" || !trusted {
		t.Errorf("observation came back as placement=%q trusted=%v, want inbox/true", placement, trusted)
	}
	if routeColumnExists(t, ctx, pool) {
		t.Error("after the rollback warmup_observations.destination_esp still exists: the down migration is not symmetric")
	}

	// Forward again on the SAME database. This is the half that catches a down
	// migration which left the constraint behind: ADD CONSTRAINT would then fail on
	// a duplicate name and the redeploy would not come up.
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	insertRouteObservation(t, ctx, pool, ws, mailbox, "route-microsoft", "microsoft")

	// The re-applied column defaults correctly for the rows that predate it, so an
	// observation from before routes existed states honestly that its destination was
	// never recorded.
	if got := readDestination(t, ctx, pool, ws, "route-google"); got != "unknown" {
		t.Errorf("a row that predates the column reads %q, want \"unknown\"", got)
	}
	if got := readDestination(t, ctx, pool, ws, "route-microsoft"); got != "microsoft" {
		t.Errorf("a row written after the re-apply reads %q, want \"microsoft\"", got)
	}
}

// seedRouteFixture creates the workspace + mailbox the observations hang off. The
// tenant FK is composite (id, workspace_id), so a real pair is required.
func seedRouteFixture(t *testing.T, ctx context.Context, pool gen.DBTX) (ws, mailbox uuid.UUID) {
	t.Helper()
	q := gen.New(pool)
	w, err := q.CreateWorkspace(ctx, "Route migration "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "warm@route.test", DisplayName: "Warm",
		SmtpHost: "smtp.route.test", SmtpPort: 587, SmtpUsername: "warm@route.test",
		ImapHost: "imap.route.test", ImapPort: 993, ImapUsername: "warm@route.test",
		SecretCiphertext: "ct", DailyCap: 50, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	return w.ID, mb.ID
}

func insertRouteObservation(t *testing.T, ctx context.Context, pool gen.DBTX, ws, mailbox uuid.UUID, key, destination string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, source,
		                                  attribution_trusted, tab_capable, idempotency_key, destination_esp)
		 VALUES ($1, $2, 'placement', 'inbox', 'warmup_receipt', true, false, $3, $4)`,
		ws, mailbox, key, destination); err != nil {
		t.Fatalf("insert route observation %s: %v", key, err)
	}
}

func readDestination(t *testing.T, ctx context.Context, pool gen.DBTX, ws uuid.UUID, key string) string {
	t.Helper()
	var got string
	if err := pool.QueryRow(ctx,
		`SELECT destination_esp FROM warmup_observations WHERE workspace_id = $1 AND idempotency_key = $2`,
		ws, key).Scan(&got); err != nil {
		t.Fatalf("read destination_esp for %s: %v", key, err)
	}
	return got
}

func routeColumnExists(t *testing.T, ctx context.Context, pool gen.DBTX) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_name = 'warmup_observations' AND column_name = 'destination_esp'`).Scan(&n); err != nil {
		t.Fatalf("read columns: %v", err)
	}
	return n > 0
}
