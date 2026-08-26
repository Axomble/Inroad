//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// Snooze against real Postgres. The rules worth verifying here are the ones
// only the database enforces: thread_id as the PRIMARY KEY (so a re-snooze
// replaces rather than duplicates), the FK cascade, and — above all — that
// `snooze_until > now()` is evaluated in SQL at read time, which is why no
// sweeper job exists.

func TestSnoozeRoundTripAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<snooze-rt@s.test>", time.Now().UTC())
	until := time.Now().UTC().Add(24 * time.Hour)

	stored, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: f.ws, ThreadID: th.ID, SnoozeUntil: until,
	})
	if err != nil {
		t.Fatalf("UpsertSnooze: %v", err)
	}
	// Postgres stores microsecond precision; compare at that granularity rather
	// than demanding an exact Go-nanosecond round trip.
	if diff := stored.SnoozeUntil.Sub(until); diff > time.Millisecond || diff < -time.Millisecond {
		t.Errorf("SnoozeUntil = %v, want ~%v", stored.SnoozeUntil, until)
	}

	got, err := f.store.GetSnooze(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetSnooze: %v", err)
	}
	if got.ThreadID != th.ID {
		t.Errorf("ThreadID = %s, want %s", got.ThreadID, th.ID)
	}

	if err := f.store.DeleteSnooze(ctx, f.ws, th.ID); err != nil {
		t.Fatalf("DeleteSnooze: %v", err)
	}
	if _, err := f.store.GetSnooze(ctx, f.ws, th.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("GetSnooze after delete = %v, want ErrNotFound", err)
	}
}

// thread_id is the PRIMARY KEY, so "one snooze per thread" is a schema
// guarantee — a re-snooze must UPDATE, not insert a second row.
func TestReSnoozeReplacesTheRowAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<snooze-replace@s.test>", time.Now().UTC())
	second := time.Now().UTC().Add(72 * time.Hour)

	for _, until := range []time.Time{time.Now().UTC().Add(24 * time.Hour), second} {
		if _, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
			WorkspaceID: f.ws, ThreadID: th.ID, SnoozeUntil: until,
		}); err != nil {
			t.Fatalf("UpsertSnooze(%v): %v", until, err)
		}
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_thread_snoozes WHERE thread_id = $1`, th.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d snooze rows after re-snoozing, want 1", rows)
	}

	got, err := f.store.GetSnooze(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetSnooze: %v", err)
	}
	if got.SnoozeUntil.Before(second.Add(-time.Second)) {
		t.Errorf("SnoozeUntil = %v, want the re-snoozed ~%v", got.SnoozeUntil, second)
	}
}

// Invariant: never read another workspace's row (docs/security.md).
func TestSnoozeIsWorkspaceScopedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<snooze-iso@s.test>", time.Now().UTC())
	if _, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: f.ws, ThreadID: th.ID, SnoozeUntil: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSnooze: %v", err)
	}

	foreignWS, _ := seedTenant(t, ctx, f.q)
	if _, err := f.store.GetSnooze(ctx, foreignWS, th.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("GetSnooze(foreign ws) = %v, want ErrNotFound", err)
	}
	if err := f.store.DeleteSnooze(ctx, foreignWS, th.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("DeleteSnooze(foreign ws) = %v, want ErrNotFound", err)
	}
	// ...and the real owner's snooze survived the foreign delete attempt.
	if _, err := f.store.GetSnooze(ctx, f.ws, th.ID); err != nil {
		t.Errorf("the owner's snooze was removed by a foreign delete: %v", err)
	}

	n, err := f.store.CountSnoozed(ctx, foreignWS)
	if err != nil {
		t.Fatalf("CountSnoozed: %v", err)
	}
	if n != 0 {
		t.Errorf("CountSnoozed(foreign ws) = %d, want 0", n)
	}
}

// The core of the design: a snooze expires by the passage of time alone. No
// sweeper runs, so an already-past snooze_until must simply stop matching.
func TestExpiredSnoozeNeedsNoSweeperAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	active := recordInbound(t, ctx, f, "<snooze-active@s.test>", time.Now().UTC())
	expired := recordInbound(t, ctx, f, "<snooze-expired@s.test>", time.Now().UTC())

	if _, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: f.ws, ThreadID: active.ID, SnoozeUntil: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSnooze(active): %v", err)
	}
	// Written directly: the Service rejects a past instant, and this represents
	// one that has since lapsed.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO inbox_thread_snoozes (thread_id, workspace_id, snooze_until) VALUES ($1, $2, $3)`,
		expired.ID, f.ws, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("insert expired snooze: %v", err)
	}

	// Only the in-force one counts.
	n, err := f.store.CountSnoozed(ctx, f.ws)
	if err != nil {
		t.Fatalf("CountSnoozed: %v", err)
	}
	if n != 1 {
		t.Errorf("CountSnoozed = %d, want 1 (the expired snooze must not count)", n)
	}

	// The snoozed scope shows only the in-force one.
	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{SnoozedOnly: true})
	if err != nil {
		t.Fatalf("ListThreads(snoozed): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != active.ID {
		t.Errorf("snoozed scope = %+v, want only %s", page.Items, active.ID)
	}

	// ...and the ordinary inbox hides that one while showing the lapsed one,
	// which has returned on its own.
	visible, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{SnoozeHidden: true})
	if err != nil {
		t.Fatalf("ListThreads(default): %v", err)
	}
	if len(visible.Items) != 1 || visible.Items[0].ID != expired.ID {
		t.Errorf("default scope = %+v, want only the lapsed-snooze thread %s", visible.Items, expired.ID)
	}
}

// Every overview counter excludes snoozed threads, because every list those
// counters label excludes them.
func TestOverviewExcludesSnoozedThreadsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	visible := recordInbound(t, ctx, f, "<ov-visible@s.test>", now)
	hidden := recordInbound(t, ctx, f, "<ov-hidden@s.test>", now)

	before, err := f.store.GetOverview(ctx, f.ws, overviewWindow())
	if err != nil {
		t.Fatalf("GetOverview(before): %v", err)
	}
	if before.Total != 2 || before.Snoozed != 0 {
		t.Fatalf("before = total %d / snoozed %d, want 2 / 0", before.Total, before.Snoozed)
	}

	if _, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: f.ws, ThreadID: hidden.ID, SnoozeUntil: now.Add(48 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSnooze: %v", err)
	}

	after, err := f.store.GetOverview(ctx, f.ws, overviewWindow())
	if err != nil {
		t.Fatalf("GetOverview(after): %v", err)
	}
	if after.Total != 1 {
		t.Errorf("Total = %d, want 1 — a snoozed thread must leave the count", after.Total)
	}
	if after.Snoozed != 1 {
		t.Errorf("Snoozed = %d, want 1", after.Snoozed)
	}
	if after.Unread != 1 || after.Today != 1 || after.AwaitingReply != 1 {
		t.Errorf("unread/today/awaiting = %d/%d/%d, want 1/1/1",
			after.Unread, after.Today, after.AwaitingReply)
	}
	// The per-mailbox breakdown must agree with the list clicking it produces.
	for _, m := range after.ByMailbox {
		if m.Total != 1 {
			t.Errorf("ByMailbox[%s].Total = %d, want 1", m.MailboxID, m.Total)
		}
	}
	_ = visible
}

// A search must still find a snoozed thread: answering "no results" because
// the operator snoozed it last week reads as data loss.
func TestSearchFindsSnoozedThreadsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<snooze-search@s.test>",
		Subject: "Quarterly invoice question", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}
	if _, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: f.ws, ThreadID: th.ID, SnoozeUntil: time.Now().UTC().Add(48 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSnooze: %v", err)
	}

	// Neither snooze flag set — how the handler builds a search.
	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{Query: "invoice"})
	if err != nil {
		t.Fatalf("ListThreads(search): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != th.ID {
		t.Errorf("search = %+v, want the snoozed thread %s", page.Items, th.ID)
	}
}

// Deleting a thread must take its snooze with it (ON DELETE CASCADE), or the
// table accumulates rows pointing at nothing.
func TestDeletingAThreadCascadesToItsSnoozeAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<snooze-cascade@s.test>", time.Now().UTC())
	if _, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: f.ws, ThreadID: th.ID, SnoozeUntil: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSnooze: %v", err)
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM inbox_threads WHERE id = $1`, th.ID); err != nil {
		t.Fatalf("delete thread: %v", err)
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_thread_snoozes WHERE thread_id = $1`, th.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d orphaned snooze rows after deleting the thread, want 0", rows)
	}
}

// A snooze must outlive the member who set it: ON DELETE SET NULL, so a
// departure never drags their teammates' snoozes back into the inbox.
func TestSnoozeSurvivesTheMemberWhoSetItAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := recordInbound(t, ctx, f, "<snooze-orphan@s.test>", time.Now().UTC())

	userID := uuid.New()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`,
		userID, "snoozer-"+uuid.NewString()+"@x.test"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := f.store.UpsertSnooze(ctx, inbox.UpsertSnoozeInput{
		WorkspaceID: f.ws, ThreadID: th.ID, SnoozeUntil: time.Now().UTC().Add(time.Hour),
		SnoozedBy: &userID,
	}); err != nil {
		t.Fatalf("UpsertSnooze: %v", err)
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	got, err := f.store.GetSnooze(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("the snooze did not survive its author's removal: %v", err)
	}
	if got.SnoozedBy != nil {
		t.Errorf("SnoozedBy = %v, want nil after the user was deleted", got.SnoozedBy)
	}
}
