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

// beforeSendsTracked is the version immediately preceding
// 20260923105515_sends_tracked_at_send_time. Named as a version, not as "one
// step down", so this keeps testing THAT migration after later ones land.
const beforeSendsTracked = 20260921111415

// The sends.tracked backfill has to reconstruct a per-send fact that was never
// stored. It can do so exactly for a send with a tracking event (the pixel or a
// rewritten link was demonstrably in the message) and otherwise falls back to
// the campaign's flag as of the migration — the signal the old query read, so
// nothing without proof changes its answer. This pins both, including the case
// that proves the event signal is not decorative: a send on a campaign whose
// tracking is off TODAY, which nevertheless recorded an event.
//
// Then down and up again on the same database, which is what a rollback and
// redeploy does. Runs on a scratch database because it moves the schema
// backwards, which would break every package sharing the test database.
func TestSendsTrackedBackfillAndRollback(t *testing.T) {
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

	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
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

	// Down removes exactly the column and nothing it hangs off.
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
	var sends int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sends WHERE workspace_id = $1`, fx.ws).Scan(&sends); err != nil {
		t.Fatalf("count sends: %v", err)
	}
	if sends != 3 {
		t.Fatalf("after the rollback %d sends remain, want all 3", sends)
	}

	// And forward again: the redeploy re-derives the same answers.
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if !readTracked(t, ctx, pool, fx.ws, fx.untrackedWithEvent) {
		t.Error("re-applied backfill lost the event-proven send")
	}
}

type trackedFixture struct {
	ws                                              uuid.UUID
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
	return fx
}

func readTracked(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, send uuid.UUID) bool {
	t.Helper()
	var tracked bool
	if err := pool.QueryRow(ctx,
		`SELECT tracked FROM sends WHERE id = $1 AND workspace_id = $2`, send, ws).Scan(&tracked); err != nil {
		t.Fatalf("read tracked: %v", err)
	}
	return tracked
}
