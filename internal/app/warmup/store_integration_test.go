//go:build integration

package warmup

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

// fixture bundles a per-test environment so the setup helper returns one value
// (keeps the helper's signature simple rather than a long result list).
type fixture struct {
	ctx   context.Context
	pool  *pgxpool.Pool
	q     *gen.Queries
	store *PgStore
	ws    gen.Workspace
	mb    gen.Mailbox
}

// setup migrates, connects, and creates a fresh workspace + mailbox for a test.
func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)

	w, err := q.CreateWorkspace(ctx, "Warmup "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "warm@x.test", DisplayName: "Warm",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: "warm@x.test",
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: "warm@x.test",
		SecretCiphertext: "ct", DailyCap: 50,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	return fixture{ctx: ctx, pool: pool, q: q, store: NewPgStore(q), ws: w, mb: mb}
}

// TestUpsertIsIdempotentAndUpdates proves enable-then-update: a second upsert
// for the same mailbox updates the ramp settings in place (not a duplicate) and
// re-flips enabled to true.
func TestUpsertIsIdempotentAndUpdates(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	first, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: w.ID,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !first.Enabled {
		t.Fatalf("first upsert: enabled=false, want true")
	}

	second, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: w.ID,
		StartVolume: 6, MaxVolume: 80, RampIncrement: 3, ReplyRate: 0.5,
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.MaxVolume != 80 || second.RampIncrement != 3 || second.StartVolume != 6 {
		t.Errorf("update not applied: %+v", second)
	}

	// The re-upsert updated in place rather than inserting a duplicate: exactly one
	// enabled participant remains for the workspace.
	n, err := store.CountEnabledParticipants(ctx, w.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("count enabled: got %d want 1", n)
	}
}

// TestCrossWorkspaceIsInvisible proves the workspace pin: workspace B cannot
// Get, and its Upsert-collision cannot overwrite, workspace A's participant.
func TestCrossWorkspaceIsInvisible(t *testing.T) {
	f := setup(t)
	ctx, q, store, w, mb := f.ctx, f.q, f.store, f.ws, f.mb

	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: w.ID,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	other, err := q.CreateWorkspace(ctx, "Other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}

	if _, err := store.GetParticipant(ctx, other.ID, mb.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace Get: got %v, want pgx.ErrNoRows", err)
	}

	// A collision upsert from the wrong workspace updates nothing and returns
	// no row (the ON CONFLICT WHERE pin filters it out), surfaced as the domain
	// sentinel.
	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: other.ID,
		StartVolume: 9, MaxVolume: 9, RampIncrement: 9, ReplyRate: 0.9,
	}); !errors.Is(err, ErrMailboxNotInWorkspace) {
		t.Fatalf("cross-workspace Upsert: got %v, want ErrMailboxNotInWorkspace", err)
	}

	// The original owner's settings are untouched.
	got, err := store.GetParticipant(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("owner Get: %v", err)
	}
	if got.MaxVolume != 40 {
		t.Errorf("owner row was mutated cross-tenant: max_volume=%d want 40", got.MaxVolume)
	}
}

// TestFirstUpsertCrossWorkspaceInsertsNothing proves the self-enforcing INSERT:
// a FIRST upsert (no pre-existing row) under a NON-owning workspace for another
// tenant's mailbox matches zero mailbox rows, so nothing is written and the store
// returns ErrMailboxNotInWorkspace — closing the gap the ON CONFLICT WHERE guard
// alone left open (it only covers a collision on an already-present row).
func TestFirstUpsertCrossWorkspaceInsertsNothing(t *testing.T) {
	f := setup(t)
	ctx, q, store, mb := f.ctx, f.q, f.store, f.mb // mb is owned by workspace A (f.ws)

	other, err := q.CreateWorkspace(ctx, "Foreign "+uuid.NewString())
	if err != nil {
		t.Fatalf("foreign workspace: %v", err)
	}

	// No participant row exists yet for mb; workspace B claiming A's mailbox must
	// insert zero rows (INSERT ... SELECT ... WHERE workspace_id = B matches none).
	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: other.ID,
		StartVolume: 9, MaxVolume: 9, RampIncrement: 9, ReplyRate: 0.9,
	}); !errors.Is(err, ErrMailboxNotInWorkspace) {
		t.Fatalf("first cross-workspace upsert: got %v, want ErrMailboxNotInWorkspace", err)
	}

	// Nothing persisted under B: B cannot see the mailbox as a participant...
	if _, err := store.GetParticipant(ctx, other.ID, mb.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("foreign upsert must persist nothing under B: Get got %v, want pgx.ErrNoRows", err)
	}
	// ...and nothing leaked under the true owner A either.
	if _, err := store.GetParticipant(ctx, f.ws.ID, mb.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("owner Get after rejected foreign upsert: got %v, want pgx.ErrNoRows", err)
	}
}

// TestDisableDeletesRow proves disable removes the participant (spec §10).
func TestDisableDeletesRow(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: w.ID,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rows, err := store.DisableParticipant(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if rows != 1 {
		t.Errorf("disable rows: got %d want 1", rows)
	}
	if _, err := store.GetParticipant(ctx, w.ID, mb.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("after disable Get: got %v want pgx.ErrNoRows", err)
	}
}

// TestStatsReads proves the stats read queries: SentToday defaults to 0 with no
// row, and the daily-series read reflects seeded counters within its UTC window.
func TestStatsReads(t *testing.T) {
	f := setup(t)
	ctx, pool, store, w, mb := f.ctx, f.pool, f.store, f.ws, f.mb

	sent, err := store.SentToday(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("sent today (empty): %v", err)
	}
	if sent != 0 {
		t.Errorf("sent today with no rows: got %d want 0", sent)
	}

	// Seed three UTC-day rows directly (no daily-stats writer exists until a later
	// step): today, an older 5-days-back row (proves ORDER BY day ASC), and a
	// 40-days-back row that must fall OUTSIDE the 30-day series window.
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_daily_stats (mailbox_id, workspace_id, day, sent, received, inbox, spam, replies)
		 VALUES ($1, $2, CURRENT_DATE,      5, 4, 3, 1, 2),
		        ($1, $2, CURRENT_DATE - 5,  1, 2, 1, 1, 0),
		        ($1, $2, CURRENT_DATE - 40, 100, 100, 100, 100, 100)`,
		mb.ID, w.ID,
	); err != nil {
		t.Fatalf("seed stats: %v", err)
	}

	sent, err = store.SentToday(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("sent today: %v", err)
	}
	if sent != 5 {
		t.Errorf("sent today: got %d want 5", sent)
	}

	// 30-day series: today + 5-days-back only; the 40-days-back row is excluded,
	// and rows come back oldest-first (ORDER BY day ASC).
	series, err := store.DailyStats(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("daily stats: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("daily series len: got %d want 2 (40-days-back row must be out of window): %+v", len(series), series)
	}
	if !series[0].Day.Time.Before(series[1].Day.Time) {
		t.Errorf("daily series not ordered day ASC: %v then %v", series[0].Day.Time, series[1].Day.Time)
	}
	if series[0].Inbox != 1 || series[1].Inbox != 3 || series[1].Spam != 1 {
		t.Errorf("daily series unexpected: %+v", series)
	}
}
