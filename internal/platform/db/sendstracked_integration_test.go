//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// The two migrations under test, named as versions rather than "one step down"
// so this keeps testing THEM after later migrations land.
const (
	beforeSendsTracked  = 20260921111415 // the version preceding both
	sendsTrackedColumn  = 20260923105515 // ADD COLUMN only
	bulkBackfilledSends = 6000           // more than one 5,000-row backfill batch
)

// sends.tracked arrives in two migrations: the column alone (a catalog change,
// so ACCESS EXCLUSIVE on sends for an instant), then a backfill that COMMITs in
// batches from inside a DO block. This walks both, forwards and back:
//
//   - the column migration writes nothing, and leaves the column NULLABLE with no
//     default, so a pre-column binary's INSERT during a rolling deploy records
//     "not recorded" (NULL) instead of a confident false;
//   - the backfill decides every NULL row — tracked if any event exists (the case
//     that proves the event signal is not decorative: a campaign untracked TODAY
//     whose send recorded one), otherwise the campaign's flag — across more than
//     one batch, which is what exercises the COMMIT inside the loop;
//   - the backfill's down is a no-op, the column's down drops it, and forward
//     again re-derives the same answers, which is what a rollback + redeploy does.
//
// Runs on a scratch database because it moves the schema backwards, which would
// break every package sharing the test database.
func TestSendsTrackedMigrationsBackfillAndRollBack(t *testing.T) {
	ctx := context.Background()
	dsn := dbtest.ScratchDSN(t, "sends_tracked_migration")

	if err := db.MigrateTo(dsn, beforeSendsTracked); err != nil {
		t.Fatalf("migrate to %d: %v", beforeSendsTracked, err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close() // before the scratch DROP, which the t.Cleanup above runs after

	fx := seedTrackedFixture(t, ctx, pool)
	total := 3 + bulkBackfilledSends

	// Column only: nothing is decided yet.
	if err := db.MigrateTo(dsn, sendsTrackedColumn); err != nil {
		t.Fatalf("migrate to the column migration: %v", err)
	}
	if n := countWhere(t, ctx, pool, fx.ws, "tracked IS NULL"); n != total {
		t.Fatalf("after the column migration %d of %d rows are NULL; it must write no rows "+
			"(the backfill is the next migration, run in batches)", n, total)
	}

	// Backfill.
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up (backfill): %v", err)
	}
	assertBackfilled(t, ctx, pool, fx, total)

	// A writer that does not name the column — a pre-column binary mid-rollout —
	// records NULL ("not recorded"), not a false it cannot know.
	var legacy *bool
	if err := pool.QueryRow(ctx,
		`INSERT INTO sends(workspace_id,campaign_id,contact_id,mailbox_id,to_email,status,step_order,sent_at)
		 SELECT workspace_id, campaign_id, contact_id, mailbox_id, to_email, 'sent', 2, now()
		   FROM sends WHERE id = $1
		 RETURNING tracked`, fx.onTracked).Scan(&legacy); err != nil {
		t.Fatalf("legacy-shaped insert: %v", err)
	}
	if legacy != nil {
		t.Fatalf("an INSERT that omits tracked recorded %v; it must be NULL so readers fall back "+
			"to the campaign's flag instead of trusting a default", *legacy)
	}
	total++

	// The backfill's down is a no-op: values survive it.
	if err := db.MigrateTo(dsn, sendsTrackedColumn); err != nil {
		t.Fatalf("migrate down past the backfill: %v", err)
	}
	if !readTracked(t, ctx, pool, fx.ws, fx.untrackedWithEvent) {
		t.Error("the backfill's down erased values; it must be a no-op (it cannot tell backfilled " +
			"values from ones a claim stamped)")
	}

	// The column's down drops exactly the column.
	if err := db.MigrateTo(dsn, beforeSendsTracked); err != nil {
		t.Fatalf("migrate down to %d: %v", beforeSendsTracked, err)
	}
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		                 WHERE table_name = 'sends' AND column_name = 'tracked')`).Scan(&exists); err != nil {
		t.Fatalf("column lookup: %v", err)
	}
	if exists {
		t.Fatal("sends.tracked survived the down migration: it is not symmetric")
	}
	if n := countWhere(t, ctx, pool, fx.ws, "true"); n != total {
		t.Fatalf("after the rollback %d sends remain, want all %d", n, total)
	}

	// And forward again: the redeploy re-derives the same answers.
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if !readTracked(t, ctx, pool, fx.ws, fx.untrackedWithEvent) {
		t.Error("re-applied backfill lost the event-proven send")
	}
	if n := countWhere(t, ctx, pool, fx.ws, "tracked IS NULL"); n != 0 {
		t.Errorf("re-applied backfill left %d rows undecided", n)
	}
}

func assertBackfilled(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fx trackedFixture, total int) {
	t.Helper()
	for _, tc := range []struct {
		name string
		send uuid.UUID
		want bool
	}{
		{"a send on a campaign tracked today", fx.onTracked, true},
		{"a send with a recorded event on a campaign untracked today", fx.untrackedWithEvent, true},
		{"a send with no event on a campaign untracked today", fx.untrackedNoEvent, false},
	} {
		if got := readTracked(t, ctx, pool, fx.ws, tc.send); got != tc.want {
			t.Errorf("%s: backfilled tracked = %v, want %v", tc.name, got, tc.want)
		}
	}
	if n := countWhere(t, ctx, pool, fx.ws, "tracked IS NULL"); n != 0 {
		t.Errorf("the backfill left %d of %d rows undecided — it stopped before the last batch", n, total)
	}
	if n := countWhere(t, ctx, pool, fx.ws, "tracked AND campaign_id = '"+fx.tracked.String()+"'"); n != 1+bulkBackfilledSends {
		t.Errorf("%d sends on the tracked campaign backfilled true, want %d (every batch)", n, 1+bulkBackfilledSends)
	}
}

type trackedFixture struct {
	ws, tracked                                     uuid.UUID
	onTracked, untrackedWithEvent, untrackedNoEvent uuid.UUID
}

// seedTrackedFixture writes raw SQL against the PRE-migration schema, so it
// uses no generated query that might already name the new column.
func seedTrackedFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) trackedFixture {
	t.Helper()
	scalar := func(sql string, args ...any) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("seed (%s): %v", sql, err)
		}
		return id
	}
	ws := scalar(`INSERT INTO workspaces(name) VALUES($1) RETURNING id`, "Tracked backfill "+uuid.NewString())
	mailbox := scalar(`INSERT INTO mailboxes(workspace_id,email,secret_ciphertext)
	 VALUES($1,'seller@tracked.test','sealed') RETURNING id`, ws)
	list := scalar(`INSERT INTO lists(workspace_id,name) VALUES($1,'L') RETURNING id`, ws)
	campaign := func(name string, tracking bool) uuid.UUID {
		return scalar(`INSERT INTO campaigns(workspace_id,name,mailbox_id,list_id,subject,status,tracking_enabled)
		 VALUES($1,$2,$3,$4,'S','running',$5) RETURNING id`, ws, name, mailbox, list, tracking)
	}
	send := func(campaignID uuid.UUID, email string) uuid.UUID {
		contact := scalar(`INSERT INTO contacts(workspace_id,email) VALUES($1,$2) RETURNING id`, ws, email)
		return scalar(`INSERT INTO sends(workspace_id,campaign_id,contact_id,mailbox_id,to_email,status,step_order,sent_at)
		 VALUES($1,$2,$3,$4,$5,'sent',1,now()) RETURNING id`, ws, campaignID, contact, mailbox, email)
	}
	tracked := campaign("Tracked", true)
	untracked := campaign("Untracked now", false)
	fx := trackedFixture{
		ws:                 ws,
		tracked:            tracked,
		onTracked:          send(tracked, "a@tracked.test"),
		untrackedWithEvent: send(untracked, "b@tracked.test"),
		untrackedNoEvent:   send(untracked, "c@tracked.test"),
	}
	// A MACHINE open: a scanner fetching the pixel still proves it was there.
	if _, err := pool.Exec(ctx,
		`INSERT INTO tracking_events(workspace_id,campaign_id,send_id,kind,user_agent,is_machine,machine_reason)
		 VALUES($1,$2,$3,'open','GoogleImageProxy',true,'proxy')`,
		ws, untracked, fx.untrackedWithEvent); err != nil {
		t.Fatalf("tracking event: %v", err)
	}
	// Enough further sends on the tracked campaign that the backfill needs more
	// than one batch, so a loop that stopped after its first COMMIT is caught.
	if _, err := pool.Exec(ctx,
		`WITH c AS (
		   INSERT INTO contacts(workspace_id,email)
		   SELECT $1, 'bulk-' || g || '@tracked.test' FROM generate_series(1, $4::int) g
		   RETURNING id, email)
		 INSERT INTO sends(workspace_id,campaign_id,contact_id,mailbox_id,to_email,status,step_order,sent_at)
		 SELECT $1, $2, c.id, $3, c.email, 'sent', 1, now() FROM c`,
		ws, tracked, mailbox, bulkBackfilledSends); err != nil {
		t.Fatalf("bulk sends: %v", err)
	}
	return fx
}

func readTracked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, send uuid.UUID) bool {
	t.Helper()
	var tracked *bool
	if err := pool.QueryRow(ctx,
		`SELECT tracked FROM sends WHERE id = $1 AND workspace_id = $2`, send, ws).Scan(&tracked); err != nil {
		t.Fatalf("read tracked: %v", err)
	}
	if tracked == nil {
		t.Fatalf("send %s is still undecided (NULL)", send)
	}
	return *tracked
}

// countWhere counts the workspace's sends matching a fixed predicate written by
// this test (never user input).
func countWhere(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws uuid.UUID, predicate string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM sends WHERE workspace_id = $1 AND (`+predicate+`)`, ws).Scan(&n); err != nil {
		t.Fatalf("count sends where %s: %v", predicate, err)
	}
	return n
}
