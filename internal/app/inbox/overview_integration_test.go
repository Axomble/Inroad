//go:build integration

package inbox_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/app/inbox"
)

// Overview counts and the virtual scope filters, against real Postgres.
//
// These exercise SQL that a unit test can only reach through a Go
// re-implementation of the same rule — in particular
// inbox_thread_awaiting_reply, whose whole purpose is to span an outbound leg
// (`sends`) that inbox_messages does not contain. A fake store that modelled
// only the stored leg would agree with the bug rather than with the read model,
// so these cases are the only real verification the rule has.
//
// Shares newFixture/seedTenant/seedTwoStepCampaign/insertSentStep with
// store_integration_test.go (same package, same build tag).

// overviewWindow is wide enough that every thread these tests seed falls
// inside both "today" and "this week", so a differing count is a real
// disagreement rather than a boundary artifact.
func overviewWindow() inbox.OverviewWindow {
	now := time.Now().UTC()
	return inbox.OverviewWindow{TodayStart: now.Add(-12 * time.Hour), WeekStart: now.AddDate(0, 0, -3)}
}

// recordInbound seeds one thread carrying a single inbound reply.
func recordInbound(t *testing.T, ctx context.Context, f *fixture, root string, at time.Time) inbox.Thread {
	t.Helper()
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: root,
		Subject: "S", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", BodyText: "hi", OccurredAt: at,
	})
	if err != nil {
		t.Fatalf("RecordReply(%s): %v", root, err)
	}
	return th
}

func TestGetOverviewCountsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()

	recordInbound(t, ctx, f, "<ov-unread@s.test>", now.Add(-time.Hour))
	read := recordInbound(t, ctx, f, "<ov-read@s.test>", now.Add(-2*time.Hour))
	if err := f.store.SetUnread(ctx, f.ws, read.ID, false); err != nil {
		t.Fatalf("SetUnread: %v", err)
	}

	got, err := f.store.GetOverview(ctx, f.ws, overviewWindow())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if got.Total != 2 {
		t.Errorf("Total = %d, want 2", got.Total)
	}
	if got.Unread != 1 {
		t.Errorf("Unread = %d, want 1", got.Unread)
	}
	if got.Today != 2 || got.ThisWeek != 2 {
		t.Errorf("Today/ThisWeek = %d/%d, want 2/2", got.Today, got.ThisWeek)
	}
	// Both threads' only message is inbound with no send answering it.
	if got.AwaitingReply != 2 {
		t.Errorf("AwaitingReply = %d, want 2", got.AwaitingReply)
	}
	if len(got.ByMailbox) != 1 {
		t.Fatalf("ByMailbox = %+v, want exactly one entry", got.ByMailbox)
	}
	if got.ByMailbox[0].MailboxID != f.mailbox || got.ByMailbox[0].Total != 2 || got.ByMailbox[0].Unread != 1 {
		t.Errorf("ByMailbox[0] = %+v, want {%s, total 2, unread 1}", got.ByMailbox[0], f.mailbox)
	}
	if len(got.ByReplyClass) != 1 || got.ByReplyClass[0].Key != "neutral" || got.ByReplyClass[0].Total != 2 {
		t.Errorf("ByReplyClass = %+v, want one {neutral, total 2} entry", got.ByReplyClass)
	}
}

// Invariant: never count another workspace's rows (docs/security.md).
func TestGetOverviewIsWorkspaceScopedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	recordInbound(t, ctx, f, "<ov-mine@s.test>", time.Now().UTC())

	foreignWS, foreignMailbox := seedTenant(t, ctx, f.q)
	if _, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: foreignWS, MailboxID: foreignMailbox, RootMessageID: "<ov-theirs@s.test>",
		Subject: "S", LastReplyClass: "positive",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("foreign RecordReply: %v", err)
	}

	got, err := f.store.GetOverview(ctx, f.ws, overviewWindow())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if got.Total != 1 {
		t.Errorf("Total = %d, want 1 — a foreign workspace's thread leaked in", got.Total)
	}
	for _, m := range got.ByMailbox {
		if m.MailboxID == foreignMailbox {
			t.Errorf("ByMailbox leaked the foreign workspace's mailbox %s", foreignMailbox)
		}
	}
	for _, c := range got.ByReplyClass {
		if c.Key == "positive" {
			t.Error("ByReplyClass leaked the foreign workspace's 'positive' class")
		}
	}
}

// The case a rule reading only inbox_messages gets wrong: the contact replied,
// then the CAMPAIGN's own follow-up step went out. That send lives in `sends`
// and is synthesized into the thread only at read time, so the thread's newest
// inbox_messages row is still the inbound reply — yet the sequence has already
// answered on our behalf.
func TestAwaitingReplyAccountsForCampaignSendsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID := seedTwoStepCampaign(t, ctx, f)
	now := time.Now().UTC()

	insertSentStep(t, ctx, f, campaignID, contactID, 1, now.Add(-3*time.Hour))
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, CampaignID: &campaignID, ContactID: &contactID,
		RootMessageID: "<awaiting@s.test>", Subject: "Intro", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", BodyText: "interested",
		OccurredAt: now.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}

	before, err := f.store.GetOverview(ctx, f.ws, overviewWindow())
	if err != nil {
		t.Fatalf("GetOverview(before): %v", err)
	}
	if before.AwaitingReply != 1 {
		t.Fatalf("AwaitingReply before the follow-up = %d, want 1", before.AwaitingReply)
	}

	// Step 2 goes out AFTER the reply: the sequence has answered.
	insertSentStep(t, ctx, f, campaignID, contactID, 2, now.Add(-time.Hour))

	after, err := f.store.GetOverview(ctx, f.ws, overviewWindow())
	if err != nil {
		t.Fatalf("GetOverview(after): %v", err)
	}
	if after.AwaitingReply != 0 {
		t.Errorf("AwaitingReply after the campaign follow-up = %d, want 0 — the `sends` row was ignored", after.AwaitingReply)
	}

	// The list's scope must agree with the counter: both go through the same
	// SQL function precisely so they cannot diverge.
	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{AwaitingReplyOnly: true})
	if err != nil {
		t.Fatalf("ListThreads(awaiting): %v", err)
	}
	for _, item := range page.Items {
		if item.ID == th.ID {
			t.Error("the awaiting_reply scope still lists a thread the campaign already followed up on")
		}
	}
}

// A manual operator reply (which DOES land in inbox_messages) clears
// "awaiting" too, so both outbound legs are treated alike.
func TestAwaitingReplyClearedByManualReplyAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	th := recordInbound(t, ctx, f, "<manual@s.test>", now.Add(-2*time.Hour))

	if err := f.store.RecordOutboundReply(ctx, th.ID, f.ws, inbox.InsertMessageInput{
		Direction: "outbound", FromEmail: "me@acme.test", BodyText: "on it",
		OccurredAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("RecordOutboundReply: %v", err)
	}

	got, err := f.store.GetOverview(ctx, f.ws, overviewWindow())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if got.AwaitingReply != 0 {
		t.Errorf("AwaitingReply = %d, want 0 after a manual reply", got.AwaitingReply)
	}
}

// A thread with no inbound message is not awaiting us — there is nothing to
// reply to. This relies on max() returning NULL and `NULL > x` being NULL,
// which FILTER/WHERE treat as false; worth pinning against real Postgres
// rather than trusting three-valued logic by inspection.
func TestAwaitingReplyExcludesThreadWithNoInboundAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	th, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<no-inbound@s.test>",
		Subject: "S", LastReplyClass: "",
	})
	if err != nil {
		t.Fatalf("UpsertThread: %v", err)
	}
	if err := f.store.RecordOutboundReply(ctx, th.ID, f.ws, inbox.InsertMessageInput{
		Direction: "outbound", FromEmail: "me@acme.test", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordOutboundReply: %v", err)
	}

	got, err := f.store.GetOverview(ctx, f.ws, overviewWindow())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if got.AwaitingReply != 0 {
		t.Errorf("AwaitingReply = %d, want 0", got.AwaitingReply)
	}
}

// The claim ListInboxThreads' own comment makes: a caller passing none of the
// new scope fields gets exactly the rows it got before they existed.
func TestUnscopedListIsUnaffectedByScopeFieldsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	for i := range 3 {
		recordInbound(t, ctx, f, "<unscoped-"+strconv.Itoa(i)+"@s.test>", now.Add(-time.Duration(i)*time.Hour))
	}
	// One read thread, so an accidental unread_only=true would show up.
	read := recordInbound(t, ctx, f, "<unscoped-read@s.test>", now.Add(-10*time.Hour))
	if err := f.store.SetUnread(ctx, f.ws, read.ID, false); err != nil {
		t.Fatalf("SetUnread: %v", err)
	}

	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(page.Items) != 4 {
		t.Errorf("unscoped ListThreads returned %d threads, want all 4", len(page.Items))
	}
}

func TestListThreadsUnreadOnlyAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	unread := recordInbound(t, ctx, f, "<scope-unread@s.test>", now)
	read := recordInbound(t, ctx, f, "<scope-read@s.test>", now.Add(-time.Hour))
	if err := f.store.SetUnread(ctx, f.ws, read.ID, false); err != nil {
		t.Fatalf("SetUnread: %v", err)
	}

	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{UnreadOnly: true})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != unread.ID {
		t.Errorf("unread-only page = %+v, want only %s", page.Items, unread.ID)
	}
}

// The lower bound the "today"/"this week" scopes turn on.
func TestListThreadsSinceLastMessageAtAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	// RecordReply stamps last_message_at with now(), so drive the bound from
	// the rows: everything just seeded is newer than `past`, none is newer
	// than `future`.
	inside := recordInbound(t, ctx, f, "<since-inside@s.test>", time.Now().UTC())

	past := time.Now().UTC().Add(-6 * time.Hour)
	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{SinceLastMessageAt: &past})
	if err != nil {
		t.Fatalf("ListThreads(past bound): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != inside.ID {
		t.Errorf("page = %+v, want only %s", page.Items, inside.ID)
	}

	future := time.Now().UTC().Add(time.Hour)
	empty, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{SinceLastMessageAt: &future})
	if err != nil {
		t.Fatalf("ListThreads(future bound): %v", err)
	}
	if len(empty.Items) != 0 {
		t.Errorf("a future lower bound returned %d threads, want 0", len(empty.Items))
	}
}

// A scope and the keyset cursor must compose: the scope bounds the set, the
// cursor names a position inside it.
func TestListThreadsScopeComposesWithCursorAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	for i := range 3 {
		recordInbound(t, ctx, f, "<compose-"+strconv.Itoa(i)+"@s.test>", now.Add(-time.Duration(i)*time.Minute))
	}

	first, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{UnreadOnly: true, Limit: 2})
	if err != nil {
		t.Fatalf("ListThreads(page 1): %v", err)
	}
	if len(first.Items) != 2 {
		t.Fatalf("page 1 = %d threads, want 2", len(first.Items))
	}
	last := first.Items[1]

	second, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{
		UnreadOnly: true, Limit: 2,
		BeforeLastMessageAt: &last.LastMessageAt, BeforeID: &last.ID,
	})
	if err != nil {
		t.Fatalf("ListThreads(page 2): %v", err)
	}
	if len(second.Items) != 1 {
		t.Fatalf("page 2 = %d threads, want the 1 remaining", len(second.Items))
	}
	for _, item := range second.Items {
		if item.ID == last.ID || item.ID == first.Items[0].ID {
			t.Errorf("page 2 repeated a thread from page 1: %s", item.ID)
		}
	}
}
