//go:build integration

package warmup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

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
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
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

// seedTransition inserts one transition row directly. The evaluator is the only
// production writer and lives in coreapi, so the store's READ is what this file
// exercises; the insert mirrors the columns that writer sets.
func seedTransition(t *testing.T, f fixture, ws, mailbox uuid.UUID, toLane string, ago time.Duration) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx,
		`INSERT INTO warmup_state_transitions (
		     workspace_id, mailbox_id, from_state, to_state, reason_code, reason,
		     from_lane, to_lane, lane_reason_code, lane_reason,
		     placement_samples, spam_rate, bounce_samples, bounce_rate,
		     complaint_samples, complaint_rate, invalid_tokens, policy_version, created_at)
		 VALUES ($1, $2, 'healthy', 'watch', 'spam_watch', 'spam placement rate above the watch threshold',
		         'healthy', $3, 'lane_watch', 'moved to watch',
		         40, 0.2, 200, 0.01, 1000, 0.0002, 0, 'warmup-phase1-v1', now() - $4::interval)`,
		ws, mailbox, toLane, ago.String(),
	); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
}

// TestListTransitionsIsNewestFirstAndWorkspacePinned proves the three properties
// the endpoint's contract rests on: ordering, the limit, and the workspace pin.
// The pin is the one that matters — the mailbox id is caller-supplied, so a row
// must never be reachable from another tenant's session.
func TestListTransitionsIsNewestFirstAndWorkspacePinned(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	seedTransition(t, f, w.ID, mb.ID, "watch", 2*time.Hour)
	seedTransition(t, f, w.ID, mb.ID, "quarantine", time.Hour)
	seedTransition(t, f, w.ID, mb.ID, "recovery", time.Minute)

	rows, err := store.ListTransitions(ctx, w.ID, mb.ID, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if rows[0].ToLane == nil || *rows[0].ToLane != "recovery" || rows[2].ToLane == nil || *rows[2].ToLane != "watch" {
		t.Fatalf("not newest-first: %v %v", *rows[0].ToLane, *rows[2].ToLane)
	}

	limited, err := store.ListTransitions(ctx, w.ID, mb.ID, 2)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit not applied: got %d want 2", len(limited))
	}

	other, err := f.q.CreateWorkspace(ctx, "Other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	foreign, err := store.ListTransitions(ctx, other.ID, mb.ID, 50)
	if err != nil {
		t.Fatalf("cross-tenant list: %v", err)
	}
	if len(foreign) != 0 {
		t.Fatalf("cross-tenant read returned %d rows", len(foreign))
	}
}

// The read path has to carry the bounce population as a THREE-valued fact:
// campaign, warmup, or genuinely unknown. Two of the three are load-bearing here —
// a labelled row must keep the label the writer chose, and a row written before the
// split must stay NULL rather than being coerced to a default that claims something
// nobody measured.
func TestListTransitionsCarriesTheBouncePopulationIncludingUnlabelledRows(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	seedTransitionWithPopulation(t, f, w.ID, mb.ID, nil, 2*time.Hour) // pre-split
	campaign := "campaign"
	seedTransitionWithPopulation(t, f, w.ID, mb.ID, &campaign, time.Hour)

	rows, err := store.ListTransitions(ctx, w.ID, mb.ID, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].BouncePopulation == nil || *rows[0].BouncePopulation != campaign {
		t.Fatalf("newest row population = %v, want campaign", rows[0].BouncePopulation)
	}
	if rows[1].BouncePopulation != nil {
		t.Fatalf("pre-split row population = %q, want NULL: the row does not know which arm spoke",
			*rows[1].BouncePopulation)
	}
}

// seedTransitionWithPopulation inserts one transition row with an explicit
// bounce_population (nil for a row written before the campaign/warmup split).
func seedTransitionWithPopulation(t *testing.T, f fixture, ws, mailbox uuid.UUID, population *string, ago time.Duration) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx,
		`INSERT INTO warmup_state_transitions (
		     workspace_id, mailbox_id, from_state, to_state, reason_code, reason,
		     placement_samples, spam_rate, bounce_population, bounce_samples, bounce_rate,
		     complaint_samples, complaint_rate, invalid_tokens, policy_version, created_at)
		 VALUES ($1, $2, 'healthy', 'throttled', 'campaign_bounce_throttle',
		         'campaign hard-bounce rate above the throttle threshold',
		         40, 0.02, $3, 200, 0.066, 1000, 0.0002, 0, 'warmup-phase1-v1', now() - $4::interval)`,
		ws, mailbox, population, ago.String(),
	); err != nil {
		t.Fatalf("seed transition: %v", err)
	}
}

// The overview read has to carry the tabbed pair with the RIGHT denominator, which
// is the half of this feature a unit test over the service cannot prove: the
// service is handed whatever the SQL counted.
//
// The fixture mixes what a real pool mixes — a Gmail mailbox whose reader can name a
// tab, and IMAP observations that structurally cannot report one — and pins the
// three ways this can go wrong: the tabbed landings must stay inside the inbox-side
// sample (dropping them would push the mailbox under MinPlacementSamples elsewhere),
// the non-capable rows must stay OUT of the tabbed denominator (pooling them dilutes
// the rate toward zero), and a spam landing must not enter it either (it is in no
// tab, so counting it would fold the spam rate into the tabbed one).
func TestOverviewRowsCarryTheTabbedPairWithItsOwnDenominator(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: w.ID,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	}); err != nil {
		t.Fatalf("seed participant: %v", err)
	}
	seedPlacementObservations(t, f, w.ID, mb.ID, "inbox", true, 6)   // Gmail, primary
	seedPlacementObservations(t, f, w.ID, mb.ID, "tabbed", true, 2)  // Gmail, a tab
	seedPlacementObservations(t, f, w.ID, mb.ID, "inbox", false, 12) // IMAP: no tab observable
	seedPlacementObservations(t, f, w.ID, mb.ID, "spam", true, 5)    // Gmail, junk: in no tab

	rows, err := store.ListOverviewRows(ctx, w.ID)
	if err != nil {
		t.Fatalf("list overview rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 overview row, got %d", len(rows))
	}
	got := rows[0]
	if got.Inbox7d != 20 {
		t.Errorf("inbox_7d = %d, want 20 (6 primary + 12 IMAP + 2 tabbed: a tabbed message landed in the inbox)", got.Inbox7d)
	}
	if got.Spam7d != 5 {
		t.Errorf("spam_7d = %d, want 5", got.Spam7d)
	}
	if got.Tabbed7d != 2 {
		t.Errorf("tabbed_7d = %d, want 2", got.Tabbed7d)
	}
	if got.TabCapable7d != 8 {
		t.Errorf("tab_capable_7d = %d, want 8 (the 6 primary + 2 tabbed Gmail observations only): "+
			"the 12 IMAP rows cannot report a tab and the 5 spam rows are in no tab", got.TabCapable7d)
	}
}

// seedPlacementObservations writes n trusted sender-attributed placement rows
// directly. The observation WRITER is exercised in coreapi's integration tests; this
// file is about the overview READ, so the rows are inserted rather than earned.
func seedPlacementObservations(t *testing.T, f fixture, ws, mailbox uuid.UUID, placement string, tabCapable bool, n int) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, tab_capable,
		                                  source, attribution_trusted, idempotency_key)
		 SELECT $1, $2, 'placement', $3::text, $4::boolean, 'warmup_receipt', true,
		        'ov:' || $3::text || ':' || $4::boolean::text || ':' || g::text
		   FROM generate_series(1, $5) g`,
		ws, mailbox, placement, tabCapable, n); err != nil {
		t.Fatalf("seed %s observations: %v", placement, err)
	}
}

// TestMailboxInWorkspaceIsTheOwnershipGate proves the 404 test: true for this
// workspace's mailbox (participant or not), false for another workspace's and
// for a mailbox that does not exist.
func TestMailboxInWorkspaceIsTheOwnershipGate(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	// Deliberately NOT a warmup participant: ownership, not participation, is the
	// gate, because the transition trail outlives the participant row.
	ok, err := store.MailboxInWorkspace(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("owned: %v", err)
	}
	if !ok {
		t.Fatalf("own mailbox must be visible even with no participant row")
	}

	other, err := f.q.CreateWorkspace(ctx, "Other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	if ok, err := store.MailboxInWorkspace(ctx, other.ID, mb.ID); err != nil || ok {
		t.Fatalf("cross-tenant ownership: ok=%v err=%v, want false/nil", ok, err)
	}
	if ok, err := store.MailboxInWorkspace(ctx, w.ID, uuid.New()); err != nil || ok {
		t.Fatalf("unknown mailbox: ok=%v err=%v, want false/nil", ok, err)
	}
}

// The overview read carries the LATEST identity per mailbox, and this is the half a
// service unit test cannot prove: the service is handed whatever the SQL picked, so
// "latest" and "attributed to the sender" are properties of the query alone.
//
// The fixture makes picking the wrong row visible rather than merely wrong: the
// stale identity and the current one disagree on every field, and a bare
// all-defaults row sits ABOVE both in time. That last row is the trap — it is what
// every observation written before identity extraction looks like, and taking it as
// "latest" would report a confidently unsigned, unauthenticated mailbox for one that
// simply had one un-extracted poll.
func TestOverviewRowsCarryTheLatestIdentityFacts(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: w.ID,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	}); err != nil {
		t.Fatalf("seed participant: %v", err)
	}
	seedIdentityObservation(t, f, w.ID, mb.ID, "id-old", "3 hours",
		"old.test", "bounce.old.test", "fail", "fail", "fail")
	seedIdentityObservation(t, f, w.ID, mb.ID, "id-new", "1 hour",
		"acme.test", "bounce.acme.test", "pass", "pass", "none")
	// Newest of all, and carrying nothing: a plain placement observation.
	seedPlacementObservations(t, f, w.ID, mb.ID, "inbox", false, 1)

	rows, err := store.ListOverviewRows(ctx, w.ID)
	if err != nil {
		t.Fatalf("list overview rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 overview row, got %d", len(rows))
	}
	got := rows[0]
	if !got.IdentityObservedAt.Valid {
		t.Fatal("identity_observed_at is NULL though two observations carried identity facts")
	}
	if got.IdentityDKIMDomain != "acme.test" || got.IdentityReturnPathDomain != "bounce.acme.test" {
		t.Errorf("domains = %q/%q, want acme.test/bounce.acme.test (the 1-hour-old row, not the 3-hour one)",
			got.IdentityDKIMDomain, got.IdentityReturnPathDomain)
	}
	if got.IdentitySPFResult != "pass" || got.IdentityDKIMResult != "pass" || got.IdentityDMARCResult != "none" {
		t.Errorf("verdicts = %q/%q/%q, want pass/pass/none: the newest row carrying facts wins, and an "+
			"all-default row is not a set of facts",
			got.IdentitySPFResult, got.IdentityDKIMResult, got.IdentityDMARCResult)
	}
}

// Null identity for a mailbox that has placement observations but none carrying
// identity facts — the state of every mailbox on the day this ships. It must not
// read as an unsigned sender whose mail failed every check, which is what the
// column defaults say if they are surfaced.
func TestOverviewRowsReportNoIdentityWhenNoneWasObserved(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: w.ID,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	}); err != nil {
		t.Fatalf("seed participant: %v", err)
	}
	seedPlacementObservations(t, f, w.ID, mb.ID, "inbox", false, 6)
	seedPlacementObservations(t, f, w.ID, mb.ID, "spam", false, 2)

	rows, err := store.ListOverviewRows(ctx, w.ID)
	if err != nil {
		t.Fatalf("list overview rows: %v", err)
	}
	got := rows[0]
	if got.IdentityObservedAt.Valid {
		t.Fatalf("identity_observed_at is set (%v) for a mailbox whose 8 observations all carry the "+
			"column defaults — the read must distinguish 'nobody looked' from 'we looked and saw nothing'",
			got.IdentityObservedAt.Time)
	}
	// The placement evidence beside it is unaffected.
	if got.Inbox7d != 6 || got.Spam7d != 2 {
		t.Errorf("inbox_7d/spam_7d = %d/%d, want 6/2", got.Inbox7d, got.Spam7d)
	}
}

// seedIdentityObservation writes one trusted sender-attributed placement row
// carrying identity facts, observed `ago` in the past.
func seedIdentityObservation(t *testing.T, f fixture, ws, mailbox uuid.UUID, key, ago,
	dkimDomain, returnPath, spf, dkim, dmarc string) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, tab_capable,
		                                  source, attribution_trusted, idempotency_key, observed_at,
		                                  dkim_domain, return_path_domain, spf_result, dkim_result, dmarc_result)
		 VALUES ($1, $2, 'placement', 'inbox', false, 'warmup_receipt', true, $3,
		         now() - $4::interval, $5, $6, $7, $8, $9)`,
		ws, mailbox, key, ago, dkimDomain, returnPath, spf, dkim, dmarc); err != nil {
		t.Fatalf("seed identity observation %s: %v", key, err)
	}
}

// The route matrix is where the per-route denominator either holds or silently
// does not, and a unit test over the service cannot prove it: the service divides
// whatever the SQL counted.
//
// The fixture makes the pooled answer visibly wrong. Google is 38 inbox / 2 spam
// (5% spam) and Microsoft is 10 / 15 (60%); pooled they read 26%, which describes
// neither route and would send an operator after the healthy one. It also mixes
// tab capability into a single route, because the tabbed rate keeps its OWN
// denominator inside the cell.
func TestListRoutesGroupsByDestinationWithPerRouteDenominators(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	// The pooled comparison at the end reads the overview, which only reports
	// participants.
	if _, err := store.UpsertParticipant(ctx, UpsertParams{
		MailboxID: mb.ID, WorkspaceID: w.ID,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
	}); err != nil {
		t.Fatalf("seed participant: %v", err)
	}
	seedRoutedObservations(t, f, w.ID, mb.ID, "google", "inbox", true, 18)
	seedRoutedObservations(t, f, w.ID, mb.ID, "google", "inbox", false, 18)
	seedRoutedObservations(t, f, w.ID, mb.ID, "google", "tabbed", true, 2)
	seedRoutedObservations(t, f, w.ID, mb.ID, "google", "spam", true, 2)
	seedRoutedObservations(t, f, w.ID, mb.ID, "microsoft", "inbox", false, 10)
	seedRoutedObservations(t, f, w.ID, mb.ID, "microsoft", "spam", false, 15)

	rows, err := store.ListRoutes(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 routes, got %d: %+v", len(rows), rows)
	}

	// Ordered by destination_esp, so the UI and every assertion below are stable.
	if rows[0].DestinationESP != "google" || rows[1].DestinationESP != "microsoft" {
		t.Fatalf("routes came back as %s, %s — want google, microsoft in destination order",
			rows[0].DestinationESP, rows[1].DestinationESP)
	}

	google, microsoft := rows[0], rows[1]
	if google.Inbox7d != 38 || google.Spam7d != 2 {
		t.Errorf("google = %d inbox / %d spam, want 38/2 (the 2 tabbed landings count on the inbox side)",
			google.Inbox7d, google.Spam7d)
	}
	if google.Tabbed7d != 2 || google.TabCapable7d != 20 {
		t.Errorf("google tabbed pair = %d/%d, want 2/20 — the tabbed rate's denominator is the "+
			"categorisable landings ON THIS ROUTE, not the route's placements and not the pool's",
			google.Tabbed7d, google.TabCapable7d)
	}
	if microsoft.Inbox7d != 10 || microsoft.Spam7d != 15 {
		t.Errorf("microsoft = %d inbox / %d spam, want 10/15", microsoft.Inbox7d, microsoft.Spam7d)
	}
	if microsoft.TabCapable7d != 0 {
		t.Errorf("microsoft tab_capable_7d = %d, want 0: nothing that observed this route could report "+
			"a category", microsoft.TabCapable7d)
	}
	// The split must sum to the pooled total the overview reports, or the matrix and
	// the headline number on the same screen disagree about the same mail.
	overview, err := store.ListOverviewRows(ctx, w.ID)
	if err != nil {
		t.Fatalf("list overview rows: %v", err)
	}
	if len(overview) != 1 {
		t.Fatalf("want 1 overview row, got %d", len(overview))
	}
	if got, want := google.Inbox7d+microsoft.Inbox7d, overview[0].Inbox7d; got != want {
		t.Errorf("routes sum to %d inbox placements, the overview reports %d: the split disagrees with "+
			"the total it came from", got, want)
	}
	if got, want := google.Spam7d+microsoft.Spam7d, overview[0].Spam7d; got != want {
		t.Errorf("routes sum to %d spam placements, the overview reports %d", got, want)
	}
}

// `unknown` and `other` are DIFFERENT routes and must never collapse into one.
// "we have not resolved this domain" and "we resolved it and it is neither Google
// nor Microsoft" are different facts; merging them would report an unmeasured
// destination as a measured one.
func TestListRoutesKeepsUnknownAndOtherApart(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	seedRoutedObservations(t, f, w.ID, mb.ID, "other", "inbox", false, 4)
	seedRoutedObservations(t, f, w.ID, mb.ID, "unknown", "inbox", false, 7)

	rows, err := store.ListRoutes(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 routes, got %d: %+v", len(rows), rows)
	}
	if rows[0].DestinationESP != "other" || rows[0].Inbox7d != 4 {
		t.Errorf("route[0] = %+v, want other with 4 inbox placements", rows[0])
	}
	if rows[1].DestinationESP != "unknown" || rows[1].Inbox7d != 7 {
		t.Errorf("route[1] = %+v, want unknown with 7 inbox placements", rows[1])
	}
}

// The window and the tenant pin, which are the two ways this read could quietly
// report someone else's mail: an observation from outside the trailing 7 days, and
// one belonging to another workspace's mailbox of the same id.
func TestListRoutesIsWindowedAndWorkspacePinned(t *testing.T) {
	f := setup(t)
	ctx, store, w, mb := f.ctx, f.store, f.ws, f.mb

	seedRoutedObservations(t, f, w.ID, mb.ID, "google", "inbox", false, 3)
	seedAgedRoutedObservations(t, f, w.ID, mb.ID, "microsoft", "inbox", 8*24*time.Hour, 5)

	rows, err := store.ListRoutes(ctx, w.ID, mb.ID)
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	if len(rows) != 1 || rows[0].DestinationESP != "google" {
		t.Fatalf("routes = %+v, want only the google route: an 8-day-old observation is outside the "+
			"7-day window", rows)
	}

	other, err := f.q.CreateWorkspace(ctx, "Route other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	foreign, err := store.ListRoutes(ctx, other.ID, mb.ID)
	if err != nil {
		t.Fatalf("list routes for the foreign workspace: %v", err)
	}
	if len(foreign) != 0 {
		t.Errorf("another workspace read %d routes for this mailbox, want 0", len(foreign))
	}
}

// seedRoutedObservations writes n trusted sender-attributed placement rows against
// one destination route, observed now.
func seedRoutedObservations(t *testing.T, f fixture, ws, mailbox uuid.UUID,
	destination, placement string, tabCapable bool, n int) {
	t.Helper()
	seedAgedRoutedObservationsWithCapability(t, f, ws, mailbox, destination, placement, tabCapable, 0, n)
}

// seedAgedRoutedObservations is the same at an explicit age, for the window.
func seedAgedRoutedObservations(t *testing.T, f fixture, ws, mailbox uuid.UUID,
	destination, placement string, age time.Duration, n int) {
	t.Helper()
	seedAgedRoutedObservationsWithCapability(t, f, ws, mailbox, destination, placement, false, age, n)
}

func seedAgedRoutedObservationsWithCapability(t *testing.T, f fixture, ws, mailbox uuid.UUID,
	destination, placement string, tabCapable bool, age time.Duration, n int) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, placement, tab_capable,
		                                  destination_esp, source, attribution_trusted,
		                                  idempotency_key, observed_at)
		 SELECT $1, $2, 'placement', $3::text, $4::boolean, $5::text, 'warmup_receipt', true,
		        'route:' || $5::text || ':' || $3::text || ':' || $4::boolean::text || ':'
		               || $6::text || ':' || g::text,
		        now() - $6::interval
		   FROM generate_series(1, $7) g`,
		ws, mailbox, placement, tabCapable, destination, age.String(), n); err != nil {
		t.Fatalf("seed %s observations to %s: %v", placement, destination, err)
	}
}
