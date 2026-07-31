//go:build integration

package campaign

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// scheduleFixture spins up a workspace + mailbox + list + campaign with contacts,
// returning the live store and ids. Mirrors the other integration fixtures here.
func scheduleFixture(t *testing.T, ctx context.Context, contacts int) (*PgStore, *gen.Queries, *pgxpool.Pool, uuid.UUID, gen.Campaign) {
	t.Helper()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	store := NewPgStore(pool)

	w, err := q.CreateWorkspace(ctx, "Sched "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: "from@x.test", DisplayName: "X",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: "from@x.test",
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: "from@x.test",
		SecretCiphertext: "ct", DailyCap: 500,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: w.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range contacts {
		c, err := q.UpsertContact(ctx, gen.UpsertContactParams{
			WorkspaceID: w.ID, Email: uuid.NewString() + "@x.test", FirstName: "C",
		})
		if err != nil {
			t.Fatalf("contact %d: %v", i, err)
		}
		if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: lst.ID, ContactID: c.ID}); err != nil {
			t.Fatalf("member %d: %v", i, err)
		}
	}
	cam, err := store.Create(ctx, w.ID, CreateInput{
		Name: "Sched", Subject: "Hi", BodyText: "b", MailboxID: mb.ID, ListID: lst.ID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return store, q, pool, w.ID, cam
}

// Create must seed the default window in the same transaction as the campaign: an
// empty week means "no valid send instant exists" to the engine, so a campaign
// must never exist without one.
func TestCreateSeedsDefaultSendWindow(t *testing.T) {
	ctx := context.Background()
	store, _, _, ws, cam := scheduleFixture(t, ctx, 1)

	windows, err := store.ListWindows(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(windows) != 5 {
		t.Fatalf("windows = %d, want 5 weekdays", len(windows))
	}
	for _, w := range windows {
		if w.Weekday == int(time.Sunday) || w.Weekday == int(time.Saturday) {
			t.Errorf("default window on weekend day %d", w.Weekday)
		}
		if w.StartMinute != 9*60 || w.EndMinute != 17*60 {
			t.Errorf("window = %d-%d, want 540-1020", w.StartMinute, w.EndMinute)
		}
	}
	if cam.Timezone != "UTC" {
		t.Errorf("timezone = %q, want UTC", cam.Timezone)
	}
	if _, err := (Schedule{Timezone: cam.Timezone, Windows: windows}).Compile(); err != nil {
		t.Errorf("seeded schedule does not compile: %v", err)
	}
}

// The database, not just Go, must reject overlapping intervals — that is what the
// send_window_no_overlap GiST exclusion constraint is for.
func TestOverlappingWindowIsRejectedByTheDatabase(t *testing.T) {
	ctx := context.Background()
	store, _, _, ws, cam := scheduleFixture(t, ctx, 1)

	// Bypass the service's validation the way a buggy caller would, writing
	// straight through the store.
	err := store.ReplaceSchedule(ctx, ws, cam.ID, Schedule{
		Timezone: "UTC",
		Windows: []SendWindow{
			{Weekday: 1, StartMinute: 540, EndMinute: 720},
			{Weekday: 1, StartMinute: 700, EndMinute: 900}, // overlaps the first
		},
	})
	if err == nil {
		t.Fatal("the database accepted overlapping windows on one weekday")
	}

	// The failed replace must roll back whole: the previous schedule survives.
	windows, lerr := store.ListWindows(ctx, ws, cam.ID)
	if lerr != nil {
		t.Fatalf("ListWindows: %v", lerr)
	}
	if len(windows) != 5 {
		t.Errorf("windows = %d after the failed replace, want the original 5", len(windows))
	}
}

// Half-open [start, end) means back-to-back intervals are adjacent, not
// overlapping — the exclusion constraint must allow them.
func TestAdjacentWindowsAreAccepted(t *testing.T) {
	ctx := context.Background()
	store, _, _, ws, cam := scheduleFixture(t, ctx, 1)

	if err := store.ReplaceSchedule(ctx, ws, cam.ID, Schedule{
		Timezone: "UTC",
		Windows: []SendWindow{
			{Weekday: 1, StartMinute: 540, EndMinute: 720},
			{Weekday: 1, StartMinute: 720, EndMinute: 1020},
		},
	}); err != nil {
		t.Fatalf("adjacent windows rejected: %v", err)
	}
	windows, err := store.ListWindows(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(windows) != 2 {
		t.Errorf("windows = %d, want 2", len(windows))
	}
}

func TestReplaceScheduleUpdatesTimezoneAndWindowsTogether(t *testing.T) {
	ctx := context.Background()
	store, q, _, ws, cam := scheduleFixture(t, ctx, 1)

	if err := store.ReplaceSchedule(ctx, ws, cam.ID, Schedule{
		Timezone: "America/New_York",
		Windows:  []SendWindow{{Weekday: 2, StartMinute: 600, EndMinute: 900}},
	}); err != nil {
		t.Fatalf("ReplaceSchedule: %v", err)
	}
	got, err := q.GetCampaign(ctx, gen.GetCampaignParams{ID: cam.ID, WorkspaceID: ws})
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	if got.Timezone != "America/New_York" {
		t.Errorf("timezone = %q", got.Timezone)
	}
	windows, err := store.ListWindows(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(windows) != 1 || windows[0].Weekday != 2 || windows[0].StartMinute != 600 {
		t.Errorf("windows = %+v, want one Tue 600-900", windows)
	}
}

// A campaign in another workspace must not be readable or writable through the
// schedule queries, even with a correct campaign id.
func TestScheduleQueriesArePinnedToTheWorkspace(t *testing.T) {
	ctx := context.Background()
	store, q, _, ws, cam := scheduleFixture(t, ctx, 1)
	other, err := q.CreateWorkspace(ctx, "Intruder "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	windows, err := store.ListWindows(ctx, other.ID, cam.ID)
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(windows) != 0 {
		t.Errorf("cross-tenant ListWindows returned %d rows", len(windows))
	}

	// A cross-tenant replace must not delete the owner's windows.
	if err := store.ReplaceSchedule(ctx, other.ID, cam.ID, Schedule{
		Timezone: "UTC", Windows: []SendWindow{{Weekday: 0, StartMinute: 0, EndMinute: 60}},
	}); err != nil {
		t.Fatalf("ReplaceSchedule: %v", err)
	}
	owned, err := store.ListWindows(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(owned) != 5 {
		t.Errorf("owner's windows = %d after a cross-tenant replace, want 5", len(owned))
	}
}

// The core regression, end to end against Postgres: a launch must land every send
// inside the window, off the clock grid, with varying gaps — not the uniform
// 2-second grid the old row_number stagger produced.
func TestLaunchStampsInWindowCadenceTimes(t *testing.T) {
	ctx := context.Background()
	const contacts = 60
	store, _, pool, ws, cam := scheduleFixture(t, ctx, contacts)

	svc := NewService(store, alwaysOKChecker{})
	res, err := svc.Launch(ctx, ws, cam.ID, noopEnqueuer{})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.TotalEnrolled != contacts {
		t.Fatalf("enrolled = %d, want %d", res.TotalEnrolled, contacts)
	}

	windows, err := store.ListWindows(ctx, ws, cam.ID)
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	win, err := (Schedule{Timezone: "UTC", Windows: windows}).Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	due, err := dueTimes(ctx, pool, ws, cam.ID)
	if err != nil {
		t.Fatalf("read due times: %v", err)
	}
	if len(due) != contacts {
		t.Fatalf("stamped due times = %d, want %d", len(due), contacts)
	}

	seen := map[time.Time]bool{}
	gaps := map[time.Duration]int{}
	var prev time.Time
	for _, at := range due {
		if !win.Contains(at) {
			t.Fatalf("send due at %s, outside the Mon–Fri 09:00–17:00 window", at.UTC())
		}
		if at.Second() == 0 && at.Nanosecond() == 0 {
			t.Fatalf("send due at %s, exactly on a clock boundary", at.UTC())
		}
		if seen[at] {
			t.Fatalf("duplicate due time %s", at.UTC())
		}
		seen[at] = true
		if !prev.IsZero() {
			gaps[at.Truncate(time.Minute).Sub(prev.Truncate(time.Minute))]++
		}
		prev = at
	}
	// The old stagger produced one identical gap for every send. Anything close to
	// that is the regression coming back.
	if len(gaps) < 3 {
		t.Errorf("only %d distinct gaps across %d sends; the spread is a grid again", len(gaps), contacts)
	}
}

// dueTimes reads the campaign's stamped next_due_at values in ascending order.
// Straight off the pool rather than through a sqlc query: next_due_at is internal
// scheduling state that no endpoint exposes, and this assertion is the only reader.
func dueTimes(ctx context.Context, pool *pgxpool.Pool, ws, campaignID uuid.UUID) ([]time.Time, error) {
	rows, err := pool.Query(ctx,
		`SELECT next_due_at FROM sequence_enrollments
		 WHERE campaign_id = $1 AND workspace_id = $2 AND next_due_at IS NOT NULL
		 ORDER BY next_due_at`, campaignID, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []time.Time{}
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			return nil, err
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

type alwaysOKChecker struct{}

func (alwaysOKChecker) MailboxActive(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}
func (alwaysOKChecker) ListExists(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

type noopEnqueuer struct{}

func (noopEnqueuer) EnqueueAdvanceAt(string, string, time.Time) error { return nil }
