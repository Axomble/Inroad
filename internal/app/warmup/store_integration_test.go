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

	list, err := store.ListParticipants(ctx, w.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want exactly 1 participant after re-upsert, got %d", len(list))
	}

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
	// no row (the ON CONFLICT WHERE pin filters it out).
	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: other.ID,
		StartVolume: 9, MaxVolume: 9, RampIncrement: 9, ReplyRate: 0.9,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-workspace Upsert: got %v, want pgx.ErrNoRows", err)
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
// row, and the daily-series / 7-day placement reads reflect seeded counters.
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

	// Seed today's counters directly (no daily-stats writer exists until a later
	// step); the read surface under test is what C1 owns.
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_daily_stats (mailbox_id, workspace_id, day, sent, received, inbox, spam, replies)
		 VALUES ($1, $2, CURRENT_DATE, 5, 4, 3, 1, 2)`,
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

	series, err := store.DailyStats(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("daily stats: %v", err)
	}
	if len(series) != 1 || series[0].Inbox != 3 || series[0].Spam != 1 {
		t.Errorf("daily series unexpected: %+v", series)
	}

	rates, err := store.PlacementRates7d(ctx, w.ID)
	if err != nil {
		t.Fatalf("placement rates: %v", err)
	}
	if len(rates) != 1 || rates[0].Inbox != 3 || rates[0].Spam != 1 || rates[0].Received != 4 {
		t.Errorf("placement rates unexpected: %+v", rates)
	}
}
