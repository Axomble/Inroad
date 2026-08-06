//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fixture is one workspace with a mailbox — the minimum UpsertThread's FKs
// need (inbox_threads.workspace_id/mailbox_id both reference real rows).
type fixture struct {
	pool    *pgxpool.Pool
	q       *gen.Queries
	store   *inbox.PgStore
	ws      uuid.UUID
	mailbox uuid.UUID
}

func newFixture(t *testing.T, ctx context.Context) *fixture {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	f := &fixture{pool: pool, q: q, store: inbox.NewPgStore(pool)}
	f.ws, f.mailbox = seedTenant(t, ctx, q)
	return f
}

// seedTenant creates an isolated workspace with one mailbox, used both as the
// primary tenant and again for a foreign one in the cross-tenant tests.
func seedTenant(t *testing.T, ctx context.Context, q *gen.Queries) (ws, mailbox uuid.UUID) {
	t.Helper()
	w, err := q.CreateWorkspace(ctx, "Inbox IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	email := uuid.NewString()[:8] + "@sender.test"
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: email, DisplayName: "IT",
		SmtpHost: "smtp.example.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.example.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 500, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	return w.ID, mb.ID
}

// threadRowCount counts inbox_threads rows for one (workspace, mailbox,
// root_message_id) — the exact key the partial unique index is built on.
func (f *fixture) threadRowCount(t *testing.T, ctx context.Context, rootMessageID string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_threads WHERE workspace_id = $1 AND mailbox_id = $2 AND root_message_id = $3`,
		f.ws, f.mailbox, rootMessageID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// The core invariant this migration exists for, against REAL Postgres (not
// the fake): the first UpsertThread for a root_message_id inserts a row;
// every later one for the SAME root_message_id updates that SAME row rather
// than inserting a second one — the ON CONFLICT ... WHERE root_message_id <>
// ” target actually matches the partial unique index sqlc vet alone cannot
// prove is wired correctly (a typo'd conflict target either errors, as the
// original ON CONFLICT ON CONSTRAINT draft did, or silently always inserts).
func TestUpsertThreadRoundTripsAndCollapsesRepeatedRepliesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	root := "<root-" + uuid.NewString() + "@sender.test>"

	first, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: root,
		Subject: "Hi there", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if !first.Unread {
		t.Error("a brand new thread must start unread")
	}
	if first.Subject != "Hi there" || first.LastReplyClass != "neutral" {
		t.Errorf("first upsert = %+v, want the inserted subject/class", first)
	}
	if got := f.threadRowCount(t, ctx, root); got != 1 {
		t.Fatalf("%d thread rows after the first upsert, want 1", got)
	}

	second, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: root,
		Subject: "Hi there (ignored on update)", LastReplyClass: "positive",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second upsert created a NEW row (%s) instead of updating %s", second.ID, first.ID)
	}
	if second.LastReplyClass != "positive" {
		t.Errorf("last_reply_class = %q after the second reply, want positive", second.LastReplyClass)
	}
	if !second.Unread {
		t.Error("a later reply must re-mark the thread unread")
	}
	// subject is NOT in the UPDATE SET list — the original subject survives.
	if second.Subject != "Hi there" {
		t.Errorf("subject = %q after the second reply, want the ORIGINAL %q (subject is not refreshed on update)",
			second.Subject, "Hi there")
	}
	if !second.LastMessageAt.After(first.LastMessageAt) && !second.LastMessageAt.Equal(first.LastMessageAt) {
		t.Errorf("last_message_at did not advance: first=%v second=%v", first.LastMessageAt, second.LastMessageAt)
	}

	// Still exactly one row: the second call updated, it did not insert.
	if got := f.threadRowCount(t, ctx, root); got != 1 {
		t.Fatalf("%d thread rows after the second upsert, want 1 (it must UPDATE, not INSERT)", got)
	}
}

// The atomicity RecordReply exists for: a failure between the thread upsert
// and the message insert must roll back BOTH, leaving neither row committed
// — not a thread whose last_reply_class/last_message_at/unread reflect a
// reply that was never actually recorded. The direction CHECK constraint
// (inbox_messages_direction_check) is the forcing function: it fails only
// AFTER the thread half of the transaction has already run, which is exactly
// the mid-transaction failure window the fix closes. A caller reaching the
// store with an invalid direction can't happen through Service (it validates
// first), but Store is a seam other callers can reach directly, and the
// database's own constraint is the backstop regardless.
func TestRecordReplyRollsBackBothRowsOnMidTransactionFailureAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	root := "<rollback-" + uuid.NewString() + "@sender.test>"

	_, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: root, Subject: "Hi", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction:  "sideways", // violates inbox_messages_direction_check
		BodyText:   "this must never land",
		OccurredAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("want an error from the invalid direction, got nil")
	}

	if got := f.threadRowCount(t, ctx, root); got != 0 {
		t.Fatalf("%d thread rows survive a rolled-back RecordReply, want 0 "+
			"(the UpsertThread half must not commit alone)", got)
	}
	var messages int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_messages WHERE workspace_id = $1`, f.ws).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messages != 0 {
		t.Fatalf("%d message rows survive a rolled-back RecordReply, want 0", messages)
	}
}

// The success path, for symmetry with the rollback test above: RecordReply
// commits BOTH rows together, and the message is reachable through the
// thread it was inserted under in the same transaction.
func TestRecordReplyCommitsBothRowsTogetherAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	root := "<commit-" + uuid.NewString() + "@sender.test>"

	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: root, Subject: "Hi", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", BodyText: "hello", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}

	detail, err := f.store.GetThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].BodyText != "hello" {
		t.Fatalf("messages = %+v, want the one RecordReply just inserted", detail.Messages)
	}
	if detail.Messages[0].WorkspaceID != f.ws || detail.Messages[0].ThreadID != th.ID {
		t.Fatalf("message = %+v, want it stamped with the transaction's own thread/workspace", detail.Messages[0])
	}
}

// A legacy match (root_message_id = "") is the documented exception: the
// partial unique index excludes it, so repeated legacy upserts must each
// insert their OWN row rather than collapsing together.
func TestUpsertThreadLegacyEmptyRootNeverCollidesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	first, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "", Subject: "A", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("first legacy upsert: %v", err)
	}
	second, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "", Subject: "B", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("second legacy upsert: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("two legacy (empty root_message_id) upserts collapsed into one row")
	}
	if got := f.threadRowCount(t, ctx, ""); got != 2 {
		t.Fatalf("%d legacy rows, want 2 (one per legacy upsert)", got)
	}
}

// The security invariant, against real Postgres: a thread id from one
// workspace resolves to NO ROW under another workspace's id — GetThread must
// not be fooled by a caller who knows a foreign thread's UUID.
func TestGetThreadCrossWorkspaceReturnsNotFoundAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<cross@sender.test>",
		Subject: "Hi", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	foreignWS, _ := seedTenant(t, ctx, f.q)
	if _, err := f.store.GetThread(ctx, foreignWS, th.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("cross-workspace GetThread error = %v, want ErrNotFound", err)
	}
	// The row is untouched and reachable under its real workspace.
	if detail, err := f.store.GetThread(ctx, f.ws, th.ID); err != nil || detail.Thread.ID != th.ID {
		t.Fatalf("GetThread under the real workspace: detail=%+v err=%v", detail, err)
	}
}

// Postgres does the idempotency, not Go: a re-poll of the same message_id
// under ON CONFLICT DO NOTHING leaves exactly one row rather than erroring or
// duplicating.
func TestInsertMessageIsIdempotentOnReplayAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<replay@sender.test>",
		Subject: "Hi", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	msg := inbox.InsertMessageInput{
		ThreadID: th.ID, WorkspaceID: f.ws, Direction: "inbound", MessageID: "<msg-1@sender.test>",
		FromEmail: "them@example.com", BodyText: "hello", OccurredAt: time.Now().UTC(),
	}
	for i := range 3 {
		if err := f.store.InsertMessage(ctx, msg); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM inbox_messages WHERE workspace_id = $1 AND message_id = $2`,
		f.ws, msg.MessageID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("%d message rows after 3 identical inserts, want 1", n)
	}
}

// Nullable campaign_id/contact_id survive the pgtype.UUID <-> *uuid.UUID
// round trip in both directions: set them and read them back non-nil; leave
// them unset (the common inbound-reply-with-no-match case) and read them
// back nil, not a zeroed UUID that would misrender as a real link.
func TestUpsertThreadNullableCampaignAndContactRoundTripAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	list, err := f.q.CreateList(ctx, gen.CreateListParams{WorkspaceID: f.ws, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	campaign, err := f.q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: f.ws, Name: "C", MailboxID: f.mailbox, ListID: list.ID, Subject: "Hi", BodyText: "b",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	contact, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "c-" + uuid.NewString() + "@x.test", FirstName: "C",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}

	withLinks, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<linked@sender.test>",
		CampaignID: &campaign.ID, ContactID: &contact.ID, Subject: "Hi", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert with links: %v", err)
	}
	if withLinks.CampaignID == nil || *withLinks.CampaignID != campaign.ID {
		t.Errorf("campaign_id = %v, want %s", withLinks.CampaignID, campaign.ID)
	}
	if withLinks.ContactID == nil || *withLinks.ContactID != contact.ID {
		t.Errorf("contact_id = %v, want %s", withLinks.ContactID, contact.ID)
	}
	// Reload through GetThread, so the SELECT side of the mapping is proven too.
	reloaded, err := f.store.GetThread(ctx, f.ws, withLinks.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if reloaded.Thread.CampaignID == nil || *reloaded.Thread.CampaignID != campaign.ID {
		t.Errorf("reloaded campaign_id = %v, want %s", reloaded.Thread.CampaignID, campaign.ID)
	}

	withoutLinks, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<unlinked@sender.test>",
		Subject: "Hi", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert without links: %v", err)
	}
	if withoutLinks.CampaignID != nil || withoutLinks.ContactID != nil {
		t.Errorf("unlinked thread = %+v, want both nil", withoutLinks)
	}
}

// ListThreads' optional filters and keyset pagination against real Postgres:
// the mailbox_id/reply_class filters actually narrow the WHERE clause, and
// the before_last_message_at/before_id pair actually walks past the named
// row rather than including it again — none of which the fake store's naive
// linear scan in service_test.go can prove.
func TestListThreadsFiltersAndPaginatesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	otherMailbox := seedMailbox(t, ctx, f.q, f.ws)

	now := time.Now().UTC()
	seed := func(mailbox uuid.UUID, root, class string, at time.Time) inbox.Thread {
		th, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
			WorkspaceID: f.ws, MailboxID: mailbox, RootMessageID: root, Subject: root, LastReplyClass: class,
		})
		if err != nil {
			t.Fatalf("seed %s: %v", root, err)
		}
		// Backdate last_message_at directly: UpsertThread always stamps now().
		if _, err := f.pool.Exec(ctx, `UPDATE inbox_threads SET last_message_at = $2 WHERE id = $1`, th.ID, at); err != nil {
			t.Fatalf("backdate %s: %v", root, err)
		}
		th.LastMessageAt = at
		return th
	}

	oldest := seed(f.mailbox, "<a@sender.test>", "neutral", now.Add(-3*time.Minute))
	middle := seed(f.mailbox, "<b@sender.test>", "positive", now.Add(-2*time.Minute))
	newest := seed(f.mailbox, "<c@sender.test>", "neutral", now.Add(-1*time.Minute))
	_ = seed(otherMailbox, "<d@sender.test>", "neutral", now) // different mailbox: excluded by the mailbox_id filter

	// Filtered by mailbox: only the three seeded on f.mailbox, newest first.
	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{MailboxID: &f.mailbox})
	if err != nil {
		t.Fatalf("ListThreads(mailbox filter): %v", err)
	}
	if len(page.Items) != 3 || page.Items[0].ID != newest.ID || page.Items[2].ID != oldest.ID {
		t.Fatalf("mailbox-filtered page = %+v, want [newest, middle, oldest]", page.Items)
	}

	// Filtered by reply_class: only the two 'neutral' ones.
	neutral := "neutral"
	byClass, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{ReplyClass: &neutral})
	if err != nil {
		t.Fatalf("ListThreads(class filter): %v", err)
	}
	if len(byClass.Items) != 3 { // includes the other-mailbox neutral thread too
		t.Fatalf("class-filtered page = %+v, want 3 neutral threads across both mailboxes", byClass.Items)
	}
	for _, it := range byClass.Items {
		if it.LastReplyClass != "neutral" {
			t.Errorf("class filter leaked a %q thread", it.LastReplyClass)
		}
	}

	// Keyset: paging with (newest.LastMessageAt, newest.ID) as the cursor
	// must return middle and oldest, and must NOT repeat newest.
	after, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{
		MailboxID: &f.mailbox, BeforeLastMessageAt: &newest.LastMessageAt, BeforeID: &newest.ID,
	})
	if err != nil {
		t.Fatalf("ListThreads(keyset): %v", err)
	}
	if len(after.Items) != 2 || after.Items[0].ID != middle.ID || after.Items[1].ID != oldest.ID {
		t.Fatalf("keyset page = %+v, want [middle, oldest] (newest must not repeat)", after.Items)
	}
}

// SetUnread is workspace-scoped in the WHERE clause itself, not merely in Go:
// a foreign workspace's SetUnread affects zero rows, and the store surfaces
// that as ErrNotFound (matching the fake store, handler_test.go's
// TestSetReadOnForeignThreadIs404, and the documented 404 on PUT
// /inbox/threads/{id}/read) rather than silently succeeding. The real thread
// is left unchanged either way.
func TestSetUnreadCrossWorkspaceReturnsNotFoundAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<setunread@sender.test>",
		Subject: "Hi", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	foreignWS, _ := seedTenant(t, ctx, f.q)
	if err := f.store.SetUnread(ctx, foreignWS, th.ID, false); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("cross-workspace SetUnread error = %v, want ErrNotFound", err)
	}
	detail, err := f.store.GetThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if !detail.Thread.Unread {
		t.Fatal("a cross-tenant SetUnread mutated another workspace's thread")
	}
}

// contact_email/contact_first_name/contact_last_name come from ListThreads
// and GetThread's LEFT JOIN on contacts, against real Postgres: a thread
// linked to a real contact reports that contact's own email/name, and a
// thread with no contact_id (root_message_id="", a legacy direct-send match)
// reports "" for all three — never NULL, never an error.
func TestListAndGetThreadReportRealContactFieldsAndEmptyStringWhenUnlinkedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	contactEmail := "ada-" + uuid.NewString() + "@x.test"
	contact, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: contactEmail, FirstName: "Ada", LastName: "Lovelace",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}

	linked, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<linked-contact@sender.test>",
		ContactID: &contact.ID, Subject: "Hi", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert linked: %v", err)
	}
	unlinked, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "", Subject: "Legacy", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert unlinked: %v", err)
	}

	// GetThread.
	detail, err := f.store.GetThread(ctx, f.ws, linked.ID)
	if err != nil {
		t.Fatalf("GetThread(linked): %v", err)
	}
	if detail.Thread.ContactEmail != contactEmail || detail.Thread.ContactFirstName != "Ada" || detail.Thread.ContactLastName != "Lovelace" {
		t.Fatalf("GetThread(linked).Thread = %+v, want the real contact fields", detail.Thread)
	}

	legacy, err := f.store.GetThread(ctx, f.ws, unlinked.ID)
	if err != nil {
		t.Fatalf("GetThread(unlinked): %v", err)
	}
	if legacy.Thread.ContactEmail != "" || legacy.Thread.ContactFirstName != "" || legacy.Thread.ContactLastName != "" {
		t.Fatalf("GetThread(unlinked).Thread = %+v, want all three contact fields \"\"", legacy.Thread)
	}

	// ListThreads.
	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{MailboxID: &f.mailbox})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[uuid.UUID]inbox.Thread{}
	for _, it := range page.Items {
		byID[it.ID] = it
	}
	gotLinked, ok := byID[linked.ID]
	if !ok {
		t.Fatalf("ListThreads did not return the linked thread: %+v", page.Items)
	}
	if gotLinked.ContactEmail != contactEmail || gotLinked.ContactFirstName != "Ada" || gotLinked.ContactLastName != "Lovelace" {
		t.Fatalf("ListThreads linked item = %+v, want the real contact fields", gotLinked)
	}
	gotUnlinked, ok := byID[unlinked.ID]
	if !ok {
		t.Fatalf("ListThreads did not return the unlinked thread: %+v", page.Items)
	}
	if gotUnlinked.ContactEmail != "" || gotUnlinked.ContactFirstName != "" || gotUnlinked.ContactLastName != "" {
		t.Fatalf("ListThreads unlinked item = %+v, want all three contact fields \"\"", gotUnlinked)
	}
}

// ListFilter.Query substring-matches (case-insensitively) the thread's
// subject OR its linked contact's email against real Postgres: a query
// matching the contact's email returns that thread, a query matching a word
// in the subject returns that thread, and a query matching neither returns
// zero results.
func TestListThreadsSearchMatchesSubjectOrContactEmailAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	contact, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "searchable-" + uuid.NewString() + "@x.test", FirstName: "S",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	byContact, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<by-contact@sender.test>",
		ContactID: &contact.ID, Subject: "Nothing special", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert by-contact: %v", err)
	}
	bySubject, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<by-subject@sender.test>",
		Subject: "Re: pricing question about widgets", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert by-subject: %v", err)
	}

	// q matching the contact's email returns that thread.
	byEmail, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{MailboxID: &f.mailbox, Query: "searchable-"})
	if err != nil {
		t.Fatalf("ListThreads(query=email substring): %v", err)
	}
	if len(byEmail.Items) != 1 || byEmail.Items[0].ID != byContact.ID {
		t.Fatalf("email-search page = %+v, want exactly [%s]", byEmail.Items, byContact.ID)
	}

	// q matching a word in the subject returns that thread.
	bySubj, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{MailboxID: &f.mailbox, Query: "widgets"})
	if err != nil {
		t.Fatalf("ListThreads(query=subject word): %v", err)
	}
	if len(bySubj.Items) != 1 || bySubj.Items[0].ID != bySubject.ID {
		t.Fatalf("subject-search page = %+v, want exactly [%s]", bySubj.Items, bySubject.ID)
	}

	// q matching neither returns zero results.
	none, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{MailboxID: &f.mailbox, Query: "no-such-match-xyz"})
	if err != nil {
		t.Fatalf("ListThreads(query=no match): %v", err)
	}
	if len(none.Items) != 0 {
		t.Fatalf("no-match page = %+v, want zero results", none.Items)
	}
}

// LIKE metacharacters typed by a user (a literal % or _) must be escaped
// before this reaches Postgres, not treated as wildcards: a query of a bare
// "%" must match ONLY the thread whose subject literally contains a "%"
// character, not every thread in the workspace.
func TestListThreadsSearchEscapesLikeMetacharactersAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	if _, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<wild1@sender.test>",
		Subject: "Ordinary subject one", LastReplyClass: "neutral",
	}); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if _, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<wild2@sender.test>",
		Subject: "50% off this week", LastReplyClass: "neutral",
	}); err != nil {
		t.Fatalf("seed 2: %v", err)
	}
	if _, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "<wild3@sender.test>",
		Subject: "widget_pro release notes", LastReplyClass: "neutral",
	}); err != nil {
		t.Fatalf("seed 3: %v", err)
	}

	page, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{MailboxID: &f.mailbox, Query: "%"})
	if err != nil {
		t.Fatalf("ListThreads(query=%%): %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Subject != "50% off this week" {
		t.Fatalf("literal-%%-search page = %+v, want exactly the ONE subject containing a literal %%", page.Items)
	}

	// A bare "_" is likewise a literal character to match, not "match any one
	// character": unescaped, it would match every subject at least one
	// character long (i.e. all three seeded threads). Only the one subject
	// that actually CONTAINS a "_" character comes back.
	underscore, err := f.store.ListThreads(ctx, f.ws, inbox.ListFilter{MailboxID: &f.mailbox, Query: "_"})
	if err != nil {
		t.Fatalf("ListThreads(query=_): %v", err)
	}
	if len(underscore.Items) != 1 || underscore.Items[0].Subject != "widget_pro release notes" {
		t.Fatalf("literal-_-search page = %+v, want exactly the ONE subject containing a literal _", underscore.Items)
	}
}

// insertSentStep inserts a 'sent' sends row directly (bypassing the claim
// machinery this test has no need to exercise) so GetThread's outbound-leg
// join has something real to find. sentAt distinguishes ordering across
// steps without relying on wall-clock timing between inserts.
func insertSentStep(t *testing.T, ctx context.Context, f *fixture, campaignID, contactID uuid.UUID, stepOrder int32, sentAt time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, message_id, sent_at, step_order)
		 VALUES ($1,$2,$3,$4,'them@example.com','sent',$5,$6,$7)`,
		f.ws, campaignID, contactID, f.mailbox, "<step-"+strconv.Itoa(int(stepOrder))+"@sender.test>", sentAt, stepOrder,
	); err != nil {
		t.Fatalf("insert sent step %d: %v", stepOrder, err)
	}
}

// seedTwoStepCampaign creates a campaign with two sequence steps (step 2's
// subject left "" — the "reply in the same thread" convention replySubject
// resolves against step 1's subject) and one enrolled contact, for the
// outbound-leg tests below.
func seedTwoStepCampaign(t *testing.T, ctx context.Context, f *fixture) (campaignID, contactID uuid.UUID) {
	t.Helper()
	list, err := f.q.CreateList(ctx, gen.CreateListParams{WorkspaceID: f.ws, Name: "L-" + uuid.NewString()})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	campaign, err := f.q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: f.ws, Name: "C", MailboxID: f.mailbox, ListID: list.ID, Subject: "Intro", BodyText: "b",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	if _, err := f.q.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: f.ws, CampaignID: campaign.ID, StepOrder: 1, Subject: "Intro", BodyText: "step one body",
	}); err != nil {
		t.Fatalf("step 1: %v", err)
	}
	if _, err := f.q.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: f.ws, CampaignID: campaign.ID, StepOrder: 2, Subject: "", BodyText: "step two body",
	}); err != nil {
		t.Fatalf("step 2: %v", err)
	}
	contact, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "outbound-" + uuid.NewString() + "@x.test", FirstName: "O",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	return campaign.ID, contact.ID
}

// The core behavior the design spec's "outbound leg" paragraph requires:
// GetThread synthesizes the outbound leg from sends/sequence_steps at read
// time (never duplicated into inbox_messages) and interleaves it with the
// stored inbound reply, ordered by time. Step 1 sent, then an inbound reply
// arrives — the reader must show BOTH, oldest first.
func TestGetThreadInterleavesOutboundStepWithInboundReplyAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID := seedTwoStepCampaign(t, ctx, f)

	sentAt := time.Now().UTC().Add(-time.Hour)
	insertSentStep(t, ctx, f, campaignID, contactID, 1, sentAt)

	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, CampaignID: &campaignID, ContactID: &contactID,
		RootMessageID: "<outbound-thread@sender.test>", Subject: "Intro", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", BodyText: "sounds good",
		OccurredAt: sentAt.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}

	detail, err := f.store.GetThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Messages) != 2 {
		t.Fatalf("messages = %+v, want exactly 2 (outbound step 1 + inbound reply)", detail.Messages)
	}
	outbound, inbound := detail.Messages[0], detail.Messages[1]
	if outbound.Direction != "outbound" || outbound.Subject != "Intro" || outbound.BodyText != "step one body" {
		t.Errorf("first message = %+v, want the outbound step 1 send", outbound)
	}
	if outbound.ToEmail != "them@example.com" || outbound.FromEmail == "" {
		t.Errorf("outbound message = %+v, want to_email/from_email populated from the send/mailbox", outbound)
	}
	if inbound.Direction != "inbound" || inbound.BodyText != "sounds good" {
		t.Errorf("second message = %+v, want the inbound reply", inbound)
	}
	if !outbound.OccurredAt.Before(inbound.OccurredAt) {
		t.Errorf("outbound.OccurredAt=%v is not before inbound.OccurredAt=%v", outbound.OccurredAt, inbound.OccurredAt)
	}
}

// A follow-up step (step 2) that left its own subject blank sends "in the
// same thread" — the outbound leg must show the SAME "Re: <step 1 subject>"
// convention the send-time path (stepsendjob.go's replySubject) actually
// applies, so the reader's subject line matches what the recipient's mail
// client really threaded on. Also proves ordering with TWO outbound steps
// plus one inbound reply.
func TestGetThreadOutboundStepWithEmptySubjectUsesReplySubjectConventionAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID := seedTwoStepCampaign(t, ctx, f)

	step1At := time.Now().UTC().Add(-2 * time.Hour)
	step2At := time.Now().UTC().Add(-time.Hour)
	insertSentStep(t, ctx, f, campaignID, contactID, 1, step1At)
	insertSentStep(t, ctx, f, campaignID, contactID, 2, step2At)

	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, CampaignID: &campaignID, ContactID: &contactID,
		RootMessageID: "<outbound-thread-2@sender.test>", Subject: "Intro", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", BodyText: "ok, replying now",
		OccurredAt: step2At.Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}

	detail, err := f.store.GetThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Messages) != 3 {
		t.Fatalf("messages = %+v, want exactly 3 (2 outbound steps + 1 inbound reply)", detail.Messages)
	}
	step1Msg, step2Msg, inboundMsg := detail.Messages[0], detail.Messages[1], detail.Messages[2]
	if step1Msg.Subject != "Intro" {
		t.Errorf("step 1 subject = %q, want its own subject %q", step1Msg.Subject, "Intro")
	}
	if step2Msg.Subject != "Re: Intro" {
		t.Errorf("step 2 subject = %q, want the same-thread convention %q", step2Msg.Subject, "Re: Intro")
	}
	if step2Msg.BodyText != "step two body" {
		t.Errorf("step 2 body = %q, want the step's real body", step2Msg.BodyText)
	}
	if inboundMsg.Direction != "inbound" {
		t.Errorf("third message direction = %q, want inbound", inboundMsg.Direction)
	}
}

// A queued (never-sent) step must NOT appear in the reader: an unsent step
// never happened yet.
func TestGetThreadExcludesUnsentStepsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID := seedTwoStepCampaign(t, ctx, f)

	step1At := time.Now().UTC().Add(-time.Hour)
	insertSentStep(t, ctx, f, campaignID, contactID, 1, step1At)
	// Step 2 is queued, never sent: no sends row for it at all (mirrors an
	// enrollment that has not yet reached its follow-up).
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, step_order)
		 VALUES ($1,$2,$3,$4,'them@example.com','queued',2)`,
		f.ws, campaignID, contactID, f.mailbox,
	); err != nil {
		t.Fatalf("insert queued step 2: %v", err)
	}

	th, err := f.store.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, CampaignID: &campaignID, ContactID: &contactID,
		RootMessageID: "<queued-step@sender.test>", Subject: "Intro", LastReplyClass: "neutral",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	detail, err := f.store.GetThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].Direction != "outbound" || detail.Messages[0].Subject != "Intro" {
		t.Fatalf("messages = %+v, want exactly the ONE sent step (the queued step 2 must not appear)", detail.Messages)
	}
}

// The legacy-direct-send case: a thread with no campaign_id/contact_id (or
// missing either one) has nothing for the outbound-leg join to match, and
// must return its stored inbound messages with no error or panic — not
// attempt a join on a nil id.
func TestGetThreadWithNoCampaignOrContactReturnsInboundOnlyAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, RootMessageID: "",
		Subject: "Legacy", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "legacy@example.com", BodyText: "a direct-send reply",
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}

	detail, err := f.store.GetThread(ctx, f.ws, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].Direction != "inbound" || detail.Messages[0].BodyText != "a direct-send reply" {
		t.Fatalf("messages = %+v, want exactly the one stored inbound message, no outbound synthesis", detail.Messages)
	}
}

// seedMailbox adds a SECOND mailbox to the workspace, for the mailbox_id
// filter test above.
func seedMailbox(t *testing.T, ctx context.Context, q *gen.Queries, ws uuid.UUID) uuid.UUID {
	t.Helper()
	email := uuid.NewString()[:8] + "@sender2.test"
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: "IT2",
		SmtpHost: "smtp.example.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.example.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 500, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("second mailbox: %v", err)
	}
	return mb.ID
}
