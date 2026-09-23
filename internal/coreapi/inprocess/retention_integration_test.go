//go:build integration

package inprocess

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/maintenance"
)

// Retention batches against real Postgres. Every guard in queries/retention.sql
// is a NOT EXISTS over another table, and a fake store would agree with any of
// them, so this is where they are actually proven.
//
// The sweeps are GLOBAL and the test database is shared, so every assertion is
// about a row this test seeded, by id — never "the batch deleted N" — except in
// the batch-boundary tests, which seed rows thousands of days old so that no
// other fixture can be in their range.

const retentionDay = 24 * time.Hour

type retentionFixture struct {
	pool                  *pgxpool.Pool
	q                     *gen.Queries
	c                     client
	ws                    uuid.UUID
	campaignID, mailboxID uuid.UUID
	listID                uuid.UUID
}

func newRetentionFixture(t *testing.T) *retentionFixture {
	t.Helper()
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	var listID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT list_id FROM campaigns WHERE id = $1`, fx.campaignID).Scan(&listID); err != nil {
		t.Fatalf("campaign list: %v", err)
	}
	// Campaigns default to 'draft'; 'done' is the neutral state for these tests
	// (not 'running', which is itself a guard).
	if _, err := pool.Exec(ctx, `UPDATE campaigns SET status = 'done' WHERE id = $1`, fx.campaignID); err != nil {
		t.Fatalf("campaign status: %v", err)
	}
	t.Cleanup(func() {
		// Deals first: their contact and campaign FKs are RESTRICT, so the
		// workspace cascade would otherwise refuse. Background context because
		// the test's own has ended by the time cleanups run.
		bg := context.Background()
		if _, err := pool.Exec(bg, `DELETE FROM deals WHERE workspace_id = ANY($1)`, []uuid.UUID{fx.ws, fx.foreignWS}); err != nil {
			t.Errorf("cleanup deals: %v", err)
		}
		if _, err := pool.Exec(bg, `DELETE FROM workspaces WHERE id = ANY($1)`, []uuid.UUID{fx.ws, fx.foreignWS}); err != nil {
			t.Errorf("cleanup workspaces: %v", err)
		}
	})
	return &retentionFixture{pool: pool, q: q, c: client{pool: pool, q: q}, ws: fx.ws, campaignID: fx.campaignID, mailboxID: fx.mailboxID, listID: listID}
}

func (f *retentionFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (f *retentionFixture) contact(t *testing.T) uuid.UUID {
	t.Helper()
	c, err := f.q.UpsertContact(context.Background(), gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "r-" + uuid.NewString() + "@x.test", FirstName: "R",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	return c.ID
}

func (f *retentionFixture) campaign(t *testing.T) uuid.UUID {
	t.Helper()
	cam, err := f.q.CreateCampaign(context.Background(), gen.CreateCampaignParams{
		WorkspaceID: f.ws, Name: "Camp " + uuid.NewString(), MailboxID: f.mailboxID, ListID: f.listID,
		Subject: "Hi", BodyText: "Hello",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	f.exec(t, `UPDATE campaigns SET status = 'done' WHERE id = $1`, cam.ID)
	return cam.ID
}

// send seeds one sends row created ageDays ago; a 'sent' row is also sent then.
func (f *retentionFixture) send(t *testing.T, campaignID, contactID uuid.UUID, ageDays int, status string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, created_at, sent_at, step_order)
		VALUES ($1, $2, $3, $4, 'r@x.test', $5, now() - make_interval(days => $6),
		        CASE WHEN $5 = 'sent' THEN now() - make_interval(days => $6) END, 1)
		RETURNING id`, f.ws, campaignID, contactID, f.mailboxID, status, ageDays).Scan(&id); err != nil {
		t.Fatalf("send: %v", err)
	}
	return id
}

// event seeds one tracking event `age` ago.
func (f *retentionFixture) event(t *testing.T, sendID uuid.UUID, kind string, machine bool, age time.Duration) {
	t.Helper()
	reason := ""
	if machine {
		reason = "test_prefetch"
	}
	f.exec(t, `
		INSERT INTO tracking_events (workspace_id, campaign_id, send_id, kind, url, user_agent, is_machine, machine_reason, client_ip, created_at)
		SELECT s.workspace_id, s.campaign_id, s.id, $2::tracking_event_kind, 'https://x.test/', 'UA', $3, $4, '203.0.113.9'::inet,
		       now() - make_interval(secs => $5)
		FROM sends s WHERE s.id = $1`, sendID, kind, machine, reason, age.Seconds())
}

func (f *retentionFixture) enroll(t *testing.T, campaignID, contactID uuid.UUID, status string) {
	t.Helper()
	f.exec(t, `INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, status) VALUES ($1, $2, $3, $4)`,
		f.ws, campaignID, contactID, status)
}

// thread seeds an inbox thread idle for ageDays, with n messages at that age.
func (f *retentionFixture) thread(t *testing.T, campaignID, contactID *uuid.UUID, ageDays, n int) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO inbox_threads (workspace_id, mailbox_id, campaign_id, contact_id, root_message_id, subject, last_message_at, created_at)
		VALUES ($1, $2, $3, $4, $5, 'Re: hi', now() - make_interval(days => $6), now() - make_interval(days => $6))
		RETURNING id`, f.ws, f.mailboxID, campaignID, contactID, "<"+uuid.NewString()+"@x.test>", ageDays).Scan(&id); err != nil {
		t.Fatalf("thread: %v", err)
	}
	for range n {
		f.message(t, id, ageDays)
	}
	return id
}

func (f *retentionFixture) message(t *testing.T, threadID uuid.UUID, ageDays int) {
	t.Helper()
	f.exec(t, `
		INSERT INTO inbox_messages (thread_id, workspace_id, mailbox_id, direction, message_id, from_email, body_text, occurred_at)
		VALUES ($1, $2, $3, 'inbound', $4, 'them@x.test', 'body', now() - make_interval(days => $5))`,
		threadID, f.ws, f.mailboxID, "<"+uuid.NewString()+"@x.test>", ageDays)
}

// deliverabilityEvent seeds one provider event ageDays old, optionally naming a send.
func (f *retentionFixture) deliverabilityEvent(t *testing.T, kind string, sendID *uuid.UUID, ageDays int) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO deliverability_events (workspace_id, kind, email, send_id, provider_event_id, bounce_class, received_at)
		VALUES ($1, $2, 'r@x.test', $3, $4, CASE WHEN $2 = 'bounce' THEN 'hard' ELSE 'unknown' END, now() - make_interval(days => $5))
		RETURNING id`, f.ws, kind, sendID, "evt-"+uuid.NewString(), ageDays).Scan(&id); err != nil {
		t.Fatalf("deliverability event: %v", err)
	}
	return id
}

func (f *retentionFixture) exists(t *testing.T, table string, id uuid.UUID) bool {
	t.Helper()
	var ok bool
	// table is always a literal from this file, never input.
	if err := f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&ok); err != nil {
		t.Fatalf("exists %s: %v", table, err)
	}
	return ok
}

type batchFn func(context.Context, coreapi.RetentionRequest) (coreapi.RetentionBatch, error)

// drain runs batches the way the sweep does — resuming from each cursor — until
// a short batch, and returns the totals.
func drain(t *testing.T, fn batchFn, window time.Duration, limit int32) coreapi.RetentionBatch {
	t.Helper()
	var total coreapi.RetentionBatch
	var cursor coreapi.RetentionCursor
	for i := 0; i < 10_000; i++ {
		res, err := fn(context.Background(), coreapi.RetentionRequest{OlderThan: window, Limit: limit, After: cursor})
		if err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
		total.Deleted += res.Deleted
		total.Dependents += res.Dependents
		total.RolledUp += res.RolledUp
		if res.Scanned < int64(limit) {
			return total
		}
		cursor = res.Next
	}
	t.Fatal("retention never drained")
	return total
}

// --- tracking events: the rollup ------------------------------------------------

type trackingReports struct {
	Engaged     []gen.CountEngagedSendsByKindRow
	ByVerdict   []gen.CountTrackingEventsByKindAndVerdictRow
	HumanOpens  int64
	SendContext gen.GetSendTrackingContextRow
	Contact     gen.ContactTrackingStatsRow
	Results     []gen.CampaignSendResultsRow
	Performance gen.ListCampaignPerformanceRow
}

// reports reads every report that aggregates tracking events.
func (f *retentionFixture) reports(t *testing.T, campaignID, sendID, contactID uuid.UUID) trackingReports {
	t.Helper()
	ctx := context.Background()
	var r trackingReports
	var err error
	if r.Engaged, err = f.q.CountEngagedSendsByKind(ctx, gen.CountEngagedSendsByKindParams{CampaignID: campaignID, WorkspaceID: f.ws}); err != nil {
		t.Fatalf("engaged: %v", err)
	}
	sort.Slice(r.Engaged, func(i, j int) bool { return r.Engaged[i].Kind < r.Engaged[j].Kind })
	if r.ByVerdict, err = f.q.CountTrackingEventsByKindAndVerdict(ctx, gen.CountTrackingEventsByKindAndVerdictParams{CampaignID: campaignID, WorkspaceID: f.ws}); err != nil {
		t.Fatalf("by verdict: %v", err)
	}
	sort.Slice(r.ByVerdict, func(i, j int) bool {
		if r.ByVerdict[i].Kind != r.ByVerdict[j].Kind {
			return r.ByVerdict[i].Kind < r.ByVerdict[j].Kind
		}
		return !r.ByVerdict[i].IsMachine && r.ByVerdict[j].IsMachine
	})
	if r.HumanOpens, err = f.q.CountHumanOpens(ctx, gen.CountHumanOpensParams{CampaignID: campaignID, WorkspaceID: f.ws}); err != nil {
		t.Fatalf("human opens: %v", err)
	}
	if r.SendContext, err = f.q.GetSendTrackingContext(ctx, sendID); err != nil {
		t.Fatalf("send context: %v", err)
	}
	if r.Contact, err = f.q.ContactTrackingStats(ctx, gen.ContactTrackingStatsParams{WorkspaceID: f.ws, ContactID: contactID}); err != nil {
		t.Fatalf("contact stats: %v", err)
	}
	if r.Results, err = f.q.CampaignSendResults(ctx, gen.CampaignSendResultsParams{CampaignID: campaignID, WorkspaceID: f.ws}); err != nil {
		t.Fatalf("results: %v", err)
	}
	perf, err := f.q.ListCampaignPerformance(ctx, f.ws)
	if err != nil {
		t.Fatalf("performance: %v", err)
	}
	for _, p := range perf {
		if p.ID == campaignID {
			r.Performance = p
		}
	}
	return r
}

type rollupRow struct {
	Kind      string
	IsMachine bool
	Events    int64
	FirstAt   time.Time
	LastAt    time.Time
}

func (f *retentionFixture) rollups(t *testing.T, sendID uuid.UUID) []rollupRow {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT kind::text, is_machine, events, first_at, last_at FROM tracking_event_rollups
		WHERE send_id = $1 ORDER BY kind, is_machine`, sendID)
	if err != nil {
		t.Fatalf("rollups: %v", err)
	}
	defer rows.Close()
	var out []rollupRow
	for rows.Next() {
		var r rollupRow
		if err := rows.Scan(&r.Kind, &r.IsMachine, &r.Events, &r.FirstAt, &r.LastAt); err != nil {
			t.Fatalf("scan rollup: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rollups: %v", err)
	}
	return out
}

func (f *retentionFixture) rawEvents(t *testing.T, sendID uuid.UUID, olderThan time.Duration) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM tracking_events WHERE send_id = $1 AND created_at < now() - make_interval(secs => $2)`,
		sendID, olderThan.Seconds()).Scan(&n); err != nil {
		t.Fatalf("raw events: %v", err)
	}
	return n
}

// THE rollup property: every report that reads tracking events says exactly the
// same thing before and after the raw rows past the window are rolled up — the
// campaign rates, the machine/human split, the per-variant results, the
// workspace ranking, the contact rollup, and the classifier's own per-send input.
// Meanwhile the raw rows (with their user agent, IP and URL) are gone.
func TestRollupTrackingEventsPreservesEveryReport(t *testing.T) {
	f := newRetentionFixture(t)
	window := 400 * retentionDay
	contactA, contactB := f.contact(t), f.contact(t)
	sendA := f.send(t, f.campaignID, contactA, 450, "sent")
	sendB := f.send(t, f.campaignID, contactB, 450, "sent")

	for _, d := range []int{420, 421, 422} {
		f.event(t, sendA, "open", false, time.Duration(d)*retentionDay)
	}
	f.event(t, sendA, "open", true, 423*retentionDay)
	f.event(t, sendA, "open", true, 423*retentionDay)
	f.event(t, sendA, "click", false, 424*retentionDay)
	f.event(t, sendA, "open", false, retentionDay) // recent: stays raw
	f.event(t, sendB, "click", true, 430*retentionDay)
	f.event(t, sendB, "open", true, 431*retentionDay)

	before := f.reports(t, f.campaignID, sendA, contactA)
	if before.SendContext.HumanOpens != 4 {
		t.Fatalf("fixture: human opens of A = %d, want 4", before.SendContext.HumanOpens)
	}

	res := drain(t, f.c.RollupTrackingEvents, window, 5000)
	if res.Deleted < 8 || res.RolledUp < 5 {
		t.Fatalf("rollup deleted %d raw rows into %d rollups, want at least this test's 8 into 5", res.Deleted, res.RolledUp)
	}

	after := f.reports(t, f.campaignID, sendA, contactA)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a report changed when its events were rolled up:\nbefore %+v\nafter  %+v", before, after)
	}
	if n := f.rawEvents(t, sendA, window); n != 0 {
		t.Errorf("%d raw events of A past the window survived", n)
	}
	if n := f.rawEvents(t, sendA, 0); n != 1 {
		t.Errorf("raw events of A = %d, want the 1 recent one", n)
	}

	got := f.rollups(t, sendA)
	if len(got) != 3 {
		t.Fatalf("rollups of A = %+v, want (click,human) (open,human) (open,machine)", got)
	}
	want := map[[2]any]int64{{"click", false}: 1, {"open", false}: 3, {"open", true}: 2}
	for _, r := range got {
		if want[[2]any{r.Kind, r.IsMachine}] != r.Events {
			t.Errorf("rollup (%s, machine=%t) events = %d, want %d", r.Kind, r.IsMachine, r.Events, want[[2]any{r.Kind, r.IsMachine}])
		}
		if r.Kind == "open" && !r.IsMachine {
			if span := r.LastAt.Sub(r.FirstAt); span < 2*retentionDay-time.Minute || span > 2*retentionDay+time.Minute {
				t.Errorf("human-open rollup spans %s, want the 2 days between its oldest and newest event", span)
			}
		}
	}

	// Idempotent: a second pass over nothing new must not count anything twice.
	drain(t, f.c.RollupTrackingEvents, window, 5000)
	if again := f.reports(t, f.campaignID, sendA, contactA); !reflect.DeepEqual(before, again) {
		t.Fatalf("a second rollup pass changed a report:\nbefore %+v\nafter  %+v", before, again)
	}
	if again := f.rollups(t, sendA); !reflect.DeepEqual(got, again) {
		t.Fatalf("a second rollup pass changed the rollups: %+v then %+v", got, again)
	}
}

// Rolling up a send whose rollup already exists must ADD to it and widen its
// time range — the ON CONFLICT path, which a single-pass test never reaches.
func TestRollupTrackingEventsMergesIntoAnExistingRollup(t *testing.T) {
	f := newRetentionFixture(t)
	s := f.send(t, f.campaignID, f.contact(t), 600, "sent")
	for _, d := range []int{520, 510, 450, 440} {
		f.event(t, s, "open", false, time.Duration(d)*retentionDay)
	}

	drain(t, f.c.RollupTrackingEvents, 500*retentionDay, 5000)
	first := f.rollups(t, s)
	if len(first) != 1 || first[0].Events != 2 {
		t.Fatalf("after the 500-day pass rollups = %+v, want one row of 2 events", first)
	}

	drain(t, f.c.RollupTrackingEvents, 400*retentionDay, 5000)
	merged := f.rollups(t, s)
	if len(merged) != 1 || merged[0].Events != 4 {
		t.Fatalf("after the 400-day pass rollups = %+v, want the same row grown to 4 events", merged)
	}
	if !merged[0].FirstAt.Equal(first[0].FirstAt) {
		t.Errorf("first_at moved from %v to %v; LEAST must keep the oldest", first[0].FirstAt, merged[0].FirstAt)
	}
	if !merged[0].LastAt.After(first[0].LastAt.Add(50 * retentionDay)) {
		t.Errorf("last_at = %v, want it advanced to the 440-day event (was %v)", merged[0].LastAt, first[0].LastAt)
	}
}

// Batch boundaries. The rows are ~8 years old so no other fixture in the shared
// database can fall in this window, which makes each batch's count exact: 7 rows
// at a limit of 3 is 3, 3, 1, and each batch resumes after the previous one's
// cursor rather than re-reading from the start.
func TestRollupTrackingEventsBatchesResumeFromTheCursor(t *testing.T) {
	f := newRetentionFixture(t)
	s := f.send(t, f.campaignID, f.contact(t), 3100, "sent")
	for d := 3001; d <= 3007; d++ {
		f.event(t, s, "open", false, time.Duration(d)*retentionDay)
	}
	window := 3000 * retentionDay
	ctx := context.Background()

	var cursor coreapi.RetentionCursor
	var counts []int64
	for range 4 {
		res, err := f.c.RollupTrackingEvents(ctx, coreapi.RetentionRequest{OlderThan: window, Limit: 3, After: cursor})
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		counts = append(counts, res.Deleted)
		if res.Deleted < 3 {
			break
		}
		if !res.Next.At.After(cursor.At) {
			t.Fatalf("cursor did not advance: %v then %v", cursor, res.Next)
		}
		cursor = res.Next
	}
	if !reflect.DeepEqual(counts, []int64{3, 3, 1}) {
		t.Fatalf("batch sizes = %v, want [3 3 1]", counts)
	}
	if got := f.rollups(t, s); len(got) != 1 || got[0].Events != 7 {
		t.Fatalf("rollups = %+v, want one row of all 7 events", got)
	}
}

// --- deliverability events --------------------------------------------------

func TestPurgeDeliverabilityEventsRespectsTheWindowAndEveryRateGuard(t *testing.T) {
	f := newRetentionFixture(t)
	window := 400 * retentionDay
	ctx := context.Background()

	oldBounce := f.deliverabilityEvent(t, "bounce", nil, 401)
	freshBounce := f.deliverabilityEvent(t, "bounce", nil, 399)
	// Two complaints past the window: only the older may go. The newer is this
	// workspace's only evidence that a complaint feed exists.
	olderComplaint := f.deliverabilityEvent(t, "complaint", nil, 402)
	newestComplaint := f.deliverabilityEvent(t, "complaint", nil, 401)

	drain(t, f.c.PurgeDeliverabilityEvents, window, 5000)
	for name, tc := range map[string]struct {
		id   uuid.UUID
		want bool
	}{
		"bounce past the window":           {oldBounce, false},
		"bounce inside the window":         {freshBounce, true},
		"older complaint past the window":  {olderComplaint, false},
		"the workspace's newest complaint": {newestComplaint, true},
	} {
		if got := f.exists(t, "deliverability_events", tc.id); got != tc.want {
			t.Errorf("%s: exists = %t, want %t", name, got, tc.want)
		}
	}
	counts, err := f.q.GetWorkspaceDeliverabilityCounts(ctx, gen.GetWorkspaceDeliverabilityCountsParams{
		WorkspaceID: f.ws, Since: pgtype.Timestamptz{Time: time.Now().Add(-7 * retentionDay), Valid: true},
	})
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if !counts.ComplaintFeed {
		t.Error("complaint_feed flipped to false: retention made a measured workspace read as NOT MEASURED")
	}

	// A running campaign's breaker can fall back to everything since
	// guardrails_enabled_at, however old. An event inside that is kept...
	campaign := f.campaign(t)
	s := f.send(t, campaign, f.contact(t), 405, "sent")
	guarded := f.deliverabilityEvent(t, "bounce", &s, 401)
	f.exec(t, `UPDATE campaigns SET status = 'running', guardrails_enabled_at = now() - interval '500 days' WHERE id = $1`, campaign)
	drain(t, f.c.PurgeDeliverabilityEvents, window, 5000)
	if !f.exists(t, "deliverability_events", guarded) {
		t.Fatal("an event inside a running campaign's breaker window was deleted")
	}
	// ...and one from before supervision began is not, even while running.
	f.exec(t, `UPDATE campaigns SET guardrails_enabled_at = now() - interval '300 days' WHERE id = $1`, campaign)
	drain(t, f.c.PurgeDeliverabilityEvents, window, 5000)
	if f.exists(t, "deliverability_events", guarded) {
		t.Fatal("an event from before the campaign's guardrails were enabled was kept")
	}
}

// --- inbox threads ----------------------------------------------------------

func TestPurgeInboxThreadsDeletesWholeIdleConversationsAndKeepsGuardedOnes(t *testing.T) {
	f := newRetentionFixture(t)
	window := 400 * retentionDay

	idle := f.thread(t, nil, nil, 401, 3)
	recent := f.thread(t, nil, nil, 10, 1)

	scheduledReply := f.thread(t, nil, nil, 401, 1)
	f.exec(t, `INSERT INTO inbox_pending_replies (workspace_id, thread_id, body_text, status, send_after)
		VALUES ($1, $2, 'queued words', 'scheduled', now() + interval '1 hour')`, f.ws, scheduledReply)
	finishedReply := f.thread(t, nil, nil, 401, 1)
	f.exec(t, `INSERT INTO inbox_pending_replies (workspace_id, thread_id, body_text, status, send_after, sent_at)
		VALUES ($1, $2, 'sent words', 'sent', now() - interval '401 days', now() - interval '401 days')`, f.ws, finishedReply)

	snoozedAhead := f.thread(t, nil, nil, 401, 1)
	f.exec(t, `INSERT INTO inbox_thread_snoozes (thread_id, workspace_id, snooze_until) VALUES ($1, $2, now() + interval '1 day')`, snoozedAhead, f.ws)
	snoozeLapsed := f.thread(t, nil, nil, 401, 1)
	f.exec(t, `INSERT INTO inbox_thread_snoozes (thread_id, workspace_id, snooze_until) VALUES ($1, $2, now() - interval '1 day')`, snoozeLapsed, f.ws)

	activeContact, doneContact := f.contact(t), f.contact(t)
	f.enroll(t, f.campaignID, activeContact, "active")
	f.enroll(t, f.campaignID, doneContact, "completed")
	activeSequence := f.thread(t, &f.campaignID, &activeContact, 401, 1)
	finishedSequence := f.thread(t, &f.campaignID, &doneContact, 401, 1)

	recentMessage := f.thread(t, nil, nil, 401, 1)
	f.message(t, recentMessage, 5) // a writer that forgot to bump last_message_at

	res := drain(t, f.c.PurgeInboxThreads, window, 5000)
	if res.Dependents < 3+1+1+1 {
		t.Errorf("messages deleted = %d, want at least the idle threads' 6", res.Dependents)
	}
	for name, tc := range map[string]struct {
		id   uuid.UUID
		want bool
	}{
		"idle thread":                         {idle, false},
		"recent thread":                       {recent, true},
		"thread with a reply still scheduled": {scheduledReply, true},
		"thread whose reply already went":     {finishedReply, false},
		"thread snoozed into the future":      {snoozedAhead, true},
		"thread whose snooze lapsed":          {snoozeLapsed, false},
		"thread of an active enrollment":      {activeSequence, true},
		"thread of a completed enrollment":    {finishedSequence, false},
		"thread with a recent message":        {recentMessage, true},
	} {
		if got := f.exists(t, "inbox_threads", tc.id); got != tc.want {
			t.Errorf("%s: exists = %t, want %t", name, got, tc.want)
		}
	}
	var orphans int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM inbox_messages WHERE thread_id = ANY($1)`,
		[]uuid.UUID{idle, finishedReply, snoozeLapsed, finishedSequence}).Scan(&orphans); err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d messages of deleted threads survived", orphans)
	}
}

// --- sends ------------------------------------------------------------------

func TestPurgeSendsDeletesOnlySendsNothingLiveDependsOn(t *testing.T) {
	f := newRetentionFixture(t)
	window := 400 * retentionDay
	ctx := context.Background()

	// Free: a completed enrollment, no thread, no event, no deal. Its tracking
	// events and rollup go with it.
	freeContact := f.contact(t)
	f.enroll(t, f.campaignID, freeContact, "completed")
	free := f.send(t, f.campaignID, freeContact, 401, "sent")
	f.event(t, free, "open", false, 401*retentionDay)
	f.event(t, free, "click", false, 401*retentionDay)
	f.exec(t, `INSERT INTO tracking_event_rollups (workspace_id, campaign_id, send_id, kind, is_machine, events, first_at, last_at)
		VALUES ($1, $2, $3, 'open', true, 5, now() - interval '500 days', now() - interval '450 days')`, f.ws, f.campaignID, free)
	failed := f.send(t, f.campaignID, f.contact(t), 401, "failed")

	recent := f.send(t, f.campaignID, f.contact(t), 399, "sent")
	queued := f.send(t, f.campaignID, f.contact(t), 401, "queued")

	activeContact := f.contact(t)
	f.enroll(t, f.campaignID, activeContact, "active")
	active := f.send(t, f.campaignID, activeContact, 401, "sent")

	threadContact := f.contact(t)
	threaded := f.send(t, f.campaignID, threadContact, 401, "sent")
	f.thread(t, &f.campaignID, &threadContact, 10, 1)

	evented := f.send(t, f.campaignID, f.contact(t), 401, "sent")
	f.deliverabilityEvent(t, "bounce", &evented, 10)

	dealContact := f.contact(t)
	dealt := f.send(t, f.campaignID, dealContact, 401, "sent")
	var pipelineID, stageID uuid.UUID
	if err := f.pool.QueryRow(ctx, `INSERT INTO pipelines (workspace_id, name) VALUES ($1, 'P') RETURNING id`, f.ws).Scan(&pipelineID); err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO pipeline_stages (pipeline_id, workspace_id, key, label, color, position)
		VALUES ($1, $2, 'new', 'New', '#888888', 1) RETURNING id`, pipelineID, f.ws).Scan(&stageID); err != nil {
		t.Fatalf("stage: %v", err)
	}
	f.exec(t, `INSERT INTO deals (workspace_id, pipeline_id, stage_id, name, primary_contact_id, source_campaign_id)
		VALUES ($1, $2, $3, 'Deal', $4, $5)`, f.ws, pipelineID, stageID, dealContact, f.campaignID)

	res := drain(t, f.c.PurgeSends, window, 5000)
	if res.Dependents < 3 {
		t.Errorf("dependent rows deleted = %d, want at least the free send's 2 events + 1 rollup", res.Dependents)
	}
	for name, tc := range map[string]struct {
		id   uuid.UUID
		want bool
	}{
		"sent, nothing depends on it":             {free, false},
		"failed, never sent":                      {failed, false},
		"inside the window":                       {recent, true},
		"still queued (the row is the claim)":     {queued, true},
		"its enrollment is active":                {active, true},
		"an inbox thread renders it":              {threaded, true},
		"a deliverability event names it":         {evented, true},
		"a deal is sourced from its conversation": {dealt, true},
	} {
		if got := f.exists(t, "sends", tc.id); got != tc.want {
			t.Errorf("%s: exists = %t, want %t", name, got, tc.want)
		}
	}
	var leftovers int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tracking_events WHERE send_id = $1) +
		(SELECT count(*) FROM tracking_event_rollups WHERE send_id = $1)`, free).Scan(&leftovers); err != nil {
		t.Fatalf("leftovers: %v", err)
	}
	if leftovers != 0 {
		t.Errorf("%d tracking rows of a deleted send survived", leftovers)
	}

	// The running-campaign guard, on its own campaign so the status flip touches
	// nothing above.
	running := f.campaign(t)
	runningSend := f.send(t, running, f.contact(t), 401, "sent")
	f.exec(t, `UPDATE campaigns SET status = 'running', guardrails_enabled_at = now() - interval '500 days' WHERE id = $1`, running)
	drain(t, f.c.PurgeSends, window, 5000)
	if !f.exists(t, "sends", runningSend) {
		t.Fatal("a send inside a running campaign's breaker window was deleted")
	}
	f.exec(t, `UPDATE campaigns SET guardrails_enabled_at = now() - interval '300 days' WHERE id = $1`, running)
	drain(t, f.c.PurgeSends, window, 5000)
	if f.exists(t, "sends", runningSend) {
		t.Fatal("a running campaign's send from before its guardrails were enabled was kept")
	}
}

// Batches are bounded by rows SCANNED, and the cursor moves past a kept row
// rather than stopping at it: ~8-year-old rows so the counts are exact. Five
// rows, the second one guarded, limit 2: batch one scans rows 0-1 and deletes
// only row 0, batch two scans and deletes 2-3, batch three scans the last row
// and is short — drained. A kept row costs one scanned slot, never a stall, and
// the batch after it resumes BEHIND it instead of re-reading it.
func TestPurgeSendsKeysetWalksPastGuardedRows(t *testing.T) {
	f := newRetentionFixture(t)
	window := 3000 * retentionDay
	var ids []uuid.UUID
	for i, age := range []int{3010, 3009, 3008, 3007, 3006} {
		c := f.contact(t)
		if i == 1 {
			f.enroll(t, f.campaignID, c, "active")
		}
		ids = append(ids, f.send(t, f.campaignID, c, age, "sent"))
	}

	ctx := context.Background()
	var cursor coreapi.RetentionCursor
	var scanned, deleted []int64
	for range 5 {
		res, err := f.c.PurgeSends(ctx, coreapi.RetentionRequest{OlderThan: window, Limit: 2, After: cursor})
		if err != nil {
			t.Fatalf("batch: %v", err)
		}
		scanned, deleted = append(scanned, res.Scanned), append(deleted, res.Deleted)
		if res.Scanned < 2 {
			break
		}
		cursor = res.Next
	}
	if !reflect.DeepEqual(scanned, []int64{2, 2, 1}) || !reflect.DeepEqual(deleted, []int64{1, 2, 1}) {
		t.Fatalf("batches scanned %v deleted %v, want scanned [2 2 1] deleted [1 2 1]", scanned, deleted)
	}
	for i, id := range ids {
		if got, want := f.exists(t, "sends", id), i == 1; got != want {
			t.Errorf("send %d exists = %t, want %t", i, got, want)
		}
	}
}

// --- the sweep end to end ---------------------------------------------------

// Disabled is a no-op against the real database, and enabled runs the tables in
// an order that frees what it can in ONE run: a send held by an old
// deliverability event and an old thread is released by those two tables'
// sweeps and deleted by the sends sweep a moment later.
func TestRetentionHandlerDisabledIsANoOpAndEnabledChainsTables(t *testing.T) {
	f := newRetentionFixture(t)
	contact := f.contact(t)
	f.enroll(t, f.campaignID, contact, "completed")
	s := f.send(t, f.campaignID, contact, 410, "sent")
	f.event(t, s, "open", false, 410*retentionDay)
	ev := f.deliverabilityEvent(t, "bounce", &s, 410)
	th := f.thread(t, &f.campaignID, &contact, 410, 2)
	dl := uuid.New()
	f.exec(t, `INSERT INTO task_dead_letters (id, workspace_id, task_type, payload, created_at)
		VALUES ($1, $2, 'sequence:advance', '{}'::jsonb, now() - interval '410 days')`, dl, f.ws)

	run := func(p maintenance.RetentionPolicy) {
		t.Helper()
		h := maintenance.RetentionHandler(f.c, p, nil, maintenance.RetentionOptions{})
		if err := h(context.Background(), asynq.NewTask(queue.TaskMaintenanceRetention, nil)); err != nil {
			t.Fatalf("handler: %v", err)
		}
	}

	run(maintenance.RetentionPolicy{})
	for table, id := range map[string]uuid.UUID{"sends": s, "deliverability_events": ev, "inbox_threads": th, "task_dead_letters": dl} {
		if !f.exists(t, table, id) {
			t.Errorf("a DISABLED policy deleted a %s row", table)
		}
	}
	if n := f.rawEvents(t, s, 0); n != 1 {
		t.Errorf("a DISABLED policy touched tracking events (%d raw left, want 1)", n)
	}

	w := 400 * retentionDay
	run(maintenance.RetentionPolicy{DeliverabilityEvents: w, InboxThreads: w, TrackingEvents: w, Sends: w, DeadLetters: w})
	for table, id := range map[string]uuid.UUID{"sends": s, "deliverability_events": ev, "inbox_threads": th, "task_dead_letters": dl} {
		if f.exists(t, table, id) {
			t.Errorf("an enabled policy left the %s row in place", table)
		}
	}
}

// --- review follow-ups --------------------------------------------------------

// A PAUSED campaign keeps its breaker history exactly as a running one does. Its
// stopped-as-bounced enrollments survive every sweep, so if its sends and events
// were pruned while it was paused, the moment it resumed the breaker would divide
// a surviving bounce numerator by a shrunken delivered denominator and pause it
// again. The assertion is on the breaker's own inputs
// (GetCampaignDeliverabilityCounts over the fallback window): identical before
// and after a purge while paused, and so after the resume.
func TestPausedCampaignKeepsItsBreakerHistoryThroughRetention(t *testing.T) {
	f := newRetentionFixture(t)
	window := 400 * retentionDay
	ctx := context.Background()
	campaign := f.campaign(t)
	f.exec(t, `UPDATE campaigns SET status = 'paused', guardrails_enabled_at = now() - interval '500 days' WHERE id = $1`, campaign)

	var sends []uuid.UUID
	for range 20 {
		sends = append(sends, f.send(t, campaign, f.contact(t), 410, "sent"))
	}
	for _, s := range sends[:3] {
		f.deliverabilityEvent(t, "bounce", &s, 405)
	}
	since := pgtype.Timestamptz{Time: time.Now().Add(-500 * retentionDay), Valid: true}
	counts := func() gen.GetCampaignDeliverabilityCountsRow {
		t.Helper()
		row, err := f.q.GetCampaignDeliverabilityCounts(ctx, gen.GetCampaignDeliverabilityCountsParams{
			WorkspaceID: f.ws, CampaignID: campaign, Since: since,
		})
		if err != nil {
			t.Fatalf("counts: %v", err)
		}
		return row
	}
	before := counts()
	if before.Delivered != 20 || before.Bounced != 3 {
		t.Fatalf("fixture: delivered=%d bounced=%d, want 20/3", before.Delivered, before.Bounced)
	}

	drain(t, f.c.PurgeDeliverabilityEvents, window, 5000)
	drain(t, f.c.PurgeSends, window, 5000)
	f.exec(t, `UPDATE campaigns SET status = 'running' WHERE id = $1`, campaign)
	if after := counts(); after != before {
		t.Fatalf("breaker inputs changed across a purge while paused: before %+v, after resume %+v", before, after)
	}
}

// The single-sweeper lock is exclusive across sessions and released cleanly, so
// a second replica's run is refused while the first holds it and succeeds after.
func TestRetentionSweepLockIsExclusiveAndReleases(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	release, ok, err := f.c.TryRetentionSweepLock(ctx)
	if err != nil || !ok {
		t.Fatalf("first lock: ok=%t err=%v", ok, err)
	}
	if _, ok2, err := f.c.TryRetentionSweepLock(ctx); err != nil || ok2 {
		t.Fatalf("second lock while held: ok=%t err=%v, want refused", ok2, err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, ok3, err := f.c.TryRetentionSweepLock(ctx)
	if err != nil || !ok3 {
		t.Fatalf("lock after release: ok=%t err=%v", ok3, err)
	}
	if err := again(); err != nil {
		t.Fatalf("second release: %v", err)
	}
}

// Progress round-trips, and the day-long cycle is judged on the database clock:
// a cycle started more than 24 hours ago reports expired, and saving with
// newCycle restarts it.
func TestRetentionProgressRoundTripsAndExpiresDaily(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	table := "test-table-" + uuid.NewString()
	t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), `DELETE FROM retention_cursors WHERE table_name = $1`, table); err != nil {
			t.Errorf("cleanup cursor: %v", err)
		}
	})

	if got, err := f.c.LoadRetentionProgress(ctx, table); err != nil || got != (coreapi.RetentionProgress{}) {
		t.Fatalf("never-swept table = %+v, %v; want the zero progress", got, err)
	}
	want := coreapi.RetentionCursor{At: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), ID: uuid.New()}
	if err := f.c.SaveRetentionProgress(ctx, table, want, false); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := f.c.LoadRetentionProgress(ctx, table)
	if err != nil || !got.Cursor.At.Equal(want.At) || got.Cursor.ID != want.ID || got.CycleExpired {
		t.Fatalf("loaded %+v, %v; want %+v, not expired", got, err, want)
	}

	f.exec(t, `UPDATE retention_cursors SET cycle_started_at = now() - interval '25 hours' WHERE table_name = $1`, table)
	if got, err := f.c.LoadRetentionProgress(ctx, table); err != nil || !got.CycleExpired {
		t.Fatalf("a 25-hour-old cycle = %+v, %v; want expired", got, err)
	}
	if err := f.c.SaveRetentionProgress(ctx, table, coreapi.RetentionCursor{}, true); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if got, err := f.c.LoadRetentionProgress(ctx, table); err != nil || got.CycleExpired {
		t.Fatalf("after a new cycle = %+v, %v; want not expired", got, err)
	}
}

// SET LOCAL scopes the batch's statement timeout to its own transaction: the
// pooled connection it ran on must come back with the server default, or every
// later query on it would inherit a one-minute ceiling.
func TestRetentionStatementTimeoutDoesNotLeakOntoThePool(t *testing.T) {
	f := newRetentionFixture(t)
	ctx := context.Background()
	var before string
	if err := f.pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&before); err != nil {
		t.Fatalf("show: %v", err)
	}
	for range 3 {
		if _, err := f.c.PurgeDeadLetters(ctx, coreapi.RetentionRequest{OlderThan: 3000 * retentionDay, Limit: 1}); err != nil {
			t.Fatalf("batch: %v", err)
		}
	}
	for range 8 { // more than the test pool's connections, so every one is checked
		var after string
		if err := f.pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&after); err != nil {
			t.Fatalf("show: %v", err)
		}
		if after != before {
			t.Fatalf("statement_timeout on a pooled connection = %q after a batch, want the default %q", after, before)
		}
	}
}
