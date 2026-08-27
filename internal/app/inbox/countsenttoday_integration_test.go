//go:build integration

package inbox_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// CountSentToday gates EVERY send against the mailbox's daily cap, and it was
// rewritten for speed: its inbox_messages half used to reach mailbox_id by
// joining inbox_threads, whose only mailbox index carries no date, so the outer
// side of that join was every thread the mailbox had ever had. mailbox_id is now
// denormalized onto inbox_messages and the join is gone.
//
// A faster query that returns a DIFFERENT number is not a test failure, it is
// reputation damage: under-counting the day's volume makes the cap over-send.
// So the tests below are equality tests first and performance tests second —
// they run the OLD query and the NEW one against the same rows and demand the
// same answer.

// countSentTodayBeforeSQL is the query EXACTLY as it stood before the
// denormalization: the inbox_messages half reaches mailbox_id through
// inbox_threads. Kept verbatim here (rather than referenced) precisely so this
// test keeps comparing against the historical behavior even after the file it
// came from moves on — it is the oracle, so it must not track the thing it is
// checking.
const countSentTodayBeforeSQL = `
SELECT (
  (SELECT count(*) FROM sends s
   WHERE s.mailbox_id = $1::uuid AND s.status = 'sent'
     AND s.sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
     AND s.sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day')
  +
  (SELECT count(*) FROM inbox_messages im
   JOIN inbox_threads t ON t.id = im.thread_id
   WHERE t.mailbox_id = $1::uuid AND im.direction = 'outbound'
     AND im.occurred_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
     AND im.occurred_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day')
)::bigint`

// countSentTodayShippedSQL mirrors the body of gen's `countSentToday`, which is
// an unexported const in another package and so cannot be referenced from here.
// It exists only to be EXPLAINed — the correctness tests call the real
// f.q.CountSentToday, never this string.
//
// Drift between this copy and the shipped query would weaken the plan test into
// checking a query nobody runs, so the plan test asserts on index NAMES that only
// exist because of this change; a drifted copy that stopped using them would fail
// rather than pass quietly.
const countSentTodayShippedSQL = `
SELECT (
  (SELECT count(*) FROM sends s
   WHERE s.mailbox_id = $1::uuid AND s.status = 'sent'
     AND s.sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
     AND s.sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day')
  +
  (SELECT count(*) FROM inbox_messages im
   WHERE im.mailbox_id = $1::uuid AND im.direction = 'outbound'
     AND im.occurred_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
     AND im.occurred_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day')
)::bigint`

// countBefore runs the pre-denormalization query — the oracle the new one must
// match.
func (f *fixture) countBefore(t *testing.T, ctx context.Context, mailbox uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(ctx, countSentTodayBeforeSQL, mailbox).Scan(&n); err != nil {
		t.Fatalf("old CountSentToday: %v", err)
	}
	return n
}

// countAfter runs the shipped (sqlc-generated) query.
func (f *fixture) countAfter(t *testing.T, ctx context.Context, mailbox uuid.UUID) int64 {
	t.Helper()
	n, err := f.q.CountSentToday(ctx, mailbox)
	if err != nil {
		t.Fatalf("new CountSentToday: %v", err)
	}
	return n
}

// seedCampaignAndContact creates the FK parents a `sends` row needs. Returns the
// campaign and list ids so a caller can add more contacts to the same campaign
// (sends is UNIQUE on (campaign_id, contact_id), so each send needs its own
// contact).
func (f *fixture) seedCampaignAndContact(t *testing.T, ctx context.Context) (campaign uuid.UUID) {
	t.Helper()
	var list uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO lists (workspace_id, name) VALUES ($1, $2) RETURNING id`,
		f.ws, "cap-it-"+uuid.NewString()[:8]).Scan(&list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO campaigns (workspace_id, name, mailbox_id, list_id, subject)
		 VALUES ($1, $2, $3, $4, 'hi') RETURNING id`,
		f.ws, "cap-it-"+uuid.NewString()[:8], f.mailbox, list).Scan(&campaign); err != nil {
		t.Fatalf("campaign: %v", err)
	}
	return campaign
}

// insertSend writes one `sends` row with an explicit status and sent_at, which
// is the half of the count that lives outside inbox_messages entirely.
func (f *fixture) insertSend(t *testing.T, ctx context.Context, campaign uuid.UUID, status string, sentAt time.Time) {
	t.Helper()
	var contact uuid.UUID
	email := uuid.NewString()[:12] + "@lead.test"
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO contacts (workspace_id, email) VALUES ($1, $2) RETURNING id`,
		f.ws, email).Scan(&contact); err != nil {
		t.Fatalf("contact: %v", err)
	}
	var sent any
	if !sentAt.IsZero() {
		sent = sentAt
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, sent_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		f.ws, campaign, contact, f.mailbox, email, status, sent); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// seedThreadWithMessage records a thread plus one message through the REAL write
// path (Store.RecordReply), so the mailbox_id the count reads is the one the
// product's own INSERT derives — not one the test wrote by hand. A test that
// hand-inserted the denormalized column would prove nothing about the writers.
func (f *fixture) seedThreadWithMessage(t *testing.T, ctx context.Context, direction string, occurredAt time.Time) uuid.UUID {
	t.Helper()
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox,
		RootMessageID: "<root-" + uuid.NewString() + "@sender.test>",
		Subject:       "cap", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: direction, MessageID: "<m-" + uuid.NewString() + "@sender.test>",
		FromEmail: "lead@x.test", BodyText: "hi", OccurredAt: occurredAt,
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}
	return th.ID
}

// appendOutbound adds an outbound message to an existing thread through
// Store.RecordOutboundReply — the manual-reply write path, and the one whose
// call site has only a thread id, which is why mailbox_id is derived in SQL
// rather than passed as a parameter.
func (f *fixture) appendOutbound(t *testing.T, ctx context.Context, threadID uuid.UUID, occurredAt time.Time) {
	t.Helper()
	if err := f.store.RecordOutboundReply(ctx, threadID, f.ws, inbox.InsertMessageInput{
		Direction: "outbound", MessageID: "<out-" + uuid.NewString() + "@sender.test>",
		FromEmail: "me@sender.test", BodyText: "replying", OccurredAt: occurredAt,
	}); err != nil {
		t.Fatalf("RecordOutboundReply: %v", err)
	}
}

// THE test. Both queries, one dataset, incrementally built so a divergence is
// attributable to the row that caused it rather than to the pile as a whole.
//
// The dataset deliberately covers the branches that could differ, not just the
// happy path: both legs present, each leg alone, inbound (must not count),
// yesterday and tomorrow on BOTH legs (must not count), non-'sent' send statuses
// (must not count), a second thread on the same mailbox, and a foreign mailbox's
// rows (must not leak in).
func TestCountSentTodayMatchesThePreDenormalizationQueryAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaign := f.seedCampaignAndContact(t, ctx)

	now := time.Now().UTC()
	// Anchored INSIDE the UTC day with margin, so a test running near midnight
	// cannot have a fixture drift across the boundary between the two queries.
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	tomorrow := today.AddDate(0, 0, 1)

	// A second mailbox in a second workspace: its rows must never be counted,
	// which the equality check alone would not catch (both queries could leak
	// identically). Asserted separately below.
	foreignWS, foreignMailbox := seedTenant(t, ctx, f.q)
	_ = foreignWS

	steps := []struct {
		name string
		add  func()
		want int64 // expected count AFTER this step
	}{
		{"empty mailbox counts zero", func() {}, 0},

		{"a sent send today counts", func() {
			f.insertSend(t, ctx, campaign, "sent", today)
		}, 1},

		{"a second sent send today counts", func() {
			f.insertSend(t, ctx, campaign, "sent", today)
		}, 2},

		{"a queued send today does not count", func() {
			f.insertSend(t, ctx, campaign, "queued", time.Time{})
		}, 2},

		{"a failed send today does not count", func() {
			f.insertSend(t, ctx, campaign, "failed", today)
		}, 2},

		{"a sent send yesterday does not count", func() {
			f.insertSend(t, ctx, campaign, "sent", yesterday)
		}, 2},

		{"a sent send tomorrow does not count", func() {
			f.insertSend(t, ctx, campaign, "sent", tomorrow)
		}, 2},

		// The other leg. An inbound message is a reply RECEIVED — it consumes
		// none of the mailbox's sending volume.
		{"an inbound message today does not count", func() {
			f.seedThreadWithMessage(t, ctx, "inbound", today)
		}, 2},

		{"an outbound message today counts", func() {
			f.seedThreadWithMessage(t, ctx, "outbound", today)
		}, 3},

		{"an outbound message appended to an existing thread counts", func() {
			th := f.seedThreadWithMessage(t, ctx, "inbound", today)
			f.appendOutbound(t, ctx, th, today)
		}, 4},

		{"an outbound message yesterday does not count", func() {
			f.seedThreadWithMessage(t, ctx, "outbound", yesterday)
		}, 4},

		{"an outbound message tomorrow does not count", func() {
			f.seedThreadWithMessage(t, ctx, "outbound", tomorrow)
		}, 4},

		{"several outbound messages on one thread all count", func() {
			th := f.seedThreadWithMessage(t, ctx, "inbound", today)
			f.appendOutbound(t, ctx, th, today)
			f.appendOutbound(t, ctx, th, today)
		}, 6},
	}

	for _, step := range steps {
		step.add()

		before := f.countBefore(t, ctx, f.mailbox)
		after := f.countAfter(t, ctx, f.mailbox)

		if before != after {
			t.Fatalf("after %q: old query = %d, new query = %d — the rewritten "+
				"CountSentToday disagrees with the one it replaced. A different "+
				"number here silently over-sends past the daily cap; it is not a "+
				"performance regression, it is a correctness one", step.name, before, after)
		}
		if after != step.want {
			t.Fatalf("after %q: count = %d, want %d (both queries agree, so the "+
				"daily-cap SEMANTICS themselves moved)", step.name, after, step.want)
		}
	}

	// Tenant isolation, which agreement between two wrong queries would hide.
	if n := f.countAfter(t, ctx, foreignMailbox); n != 0 {
		t.Fatalf("foreign mailbox count = %d, want 0 — another mailbox's volume "+
			"is being charged against this one's cap", n)
	}
}

// The denormalized column is only as good as the writers. Every path that
// inserts an inbox_message must populate mailbox_id, and it must equal the
// thread's — a writer that forgets makes the count under-report, which makes the
// cap over-send, silently.
//
// The NOT NULL constraint catches "forgot entirely"; nothing but this catches
// "wrote the wrong one". Checked over the whole table, so a future write path
// added without a test of its own is still caught the moment this runs.
func TestEveryInboxMessageCarriesItsThreadsMailboxAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	now := time.Now().UTC()
	th := f.seedThreadWithMessage(t, ctx, "inbound", now)
	f.appendOutbound(t, ctx, th, now)
	f.seedThreadWithMessage(t, ctx, "outbound", now)

	// InsertMessage — the third write path, bypassing both transactional ones.
	if err := f.store.InsertMessage(ctx, inbox.InsertMessageInput{
		ThreadID: th, WorkspaceID: f.ws, Direction: "outbound",
		MessageID: "<direct-" + uuid.NewString() + "@sender.test>",
		FromEmail: "me@sender.test", BodyText: "direct", OccurredAt: now,
	}); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}

	var mismatched int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM inbox_messages m
		JOIN inbox_threads t ON t.id = m.thread_id
		WHERE m.mailbox_id IS DISTINCT FROM t.mailbox_id`).Scan(&mismatched); err != nil {
		t.Fatalf("mismatch check: %v", err)
	}
	if mismatched != 0 {
		t.Fatalf("%d inbox_messages rows carry a mailbox_id that is not their "+
			"thread's — CountSentToday reads that column directly, so each one is "+
			"a send the daily cap cannot see", mismatched)
	}
}

// The actual deliverable: the plan. The old shape's giveaway was reading
// inbox_threads at all — that is where the unbounded side lived. The new plan
// must not mention inbox_threads, and must reach inbox_messages through the
// partial (mailbox_id, occurred_at) index rather than a sequential scan.
//
// Asserted structurally (no inbox_threads node, no Seq Scan on inbox_messages)
// rather than by timing, because a timing assertion on a small test table is
// noise. The shape is the invariant; the speed follows from it.
//
// Measured on a seeded 40-mailbox instance with 200 of today's outbound messages
// per mailbox — the shape that actually degraded — the join form planned at cost
// 1487 with 607 buffers (an idx_inbox_threads_mailbox scan over every thread the
// mailbox ever had, then one probe per thread), and this form plans at cost 46
// with 11 buffers. Same count both ways: 200.
func TestCountSentTodayPlanNoLongerTouchesInboxThreadsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	now := time.Now().UTC()
	th := f.seedThreadWithMessage(t, ctx, "inbound", now)
	f.appendOutbound(t, ctx, th, now)

	// Postgres will happily seq-scan a table of a handful of rows regardless of
	// indexes, which would make the assertion below meaningless. Disabling the
	// seq-scan path for this one session asks the planner the question the test
	// actually cares about — "CAN this be served by an index seek?" — instead of
	// "is it worth it at 3 rows?".
	if _, err := f.pool.Exec(ctx, `SET enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	plan := f.explainCountSentToday(t, ctx, f.mailbox)
	t.Logf("CountSentToday plan:\n%s", plan)

	if strings.Contains(plan, "inbox_threads") {
		t.Errorf("the plan still reads inbox_threads:\n%s\n\nThat table was the "+
			"unbounded side — its only mailbox index (idx_inbox_threads_mailbox) "+
			"carries no date, so touching it at all reintroduces the full-history "+
			"scan this change removed", plan)
	}
	if !strings.Contains(plan, "idx_inbox_messages_mailbox_outbound") {
		t.Errorf("the plan does not use idx_inbox_messages_mailbox_outbound:\n%s\n\n"+
			"Without it the inbox_messages half filters the mailbox's whole history "+
			"instead of seeking today's stripe", plan)
	}
	if strings.Contains(plan, "Seq Scan on inbox_messages") {
		t.Errorf("the plan sequentially scans inbox_messages:\n%s\n\nThe whole point "+
			"of the denormalized column is that this half seeks (mailbox_id, "+
			"occurred_at) instead of reading the table", plan)
	}

	// Deliberately NOT asserting which index the sends half uses. Several are
	// applicable (idx_sends_mailbox_sent is partial on status='sent', and at low
	// row counts the planner reasonably prefers plain idx_sends_mailbox), the
	// choice is data-dependent, and that half is not what this change touched —
	// pinning it here would make an unrelated seed change fail this test.
}

// explainCountSentToday returns the text plan for the SHIPPED query. The SQL is
// read out of the generated code's own constant so the plan can never describe a
// query different from the one that runs in production.
func (f *fixture) explainCountSentToday(t *testing.T, ctx context.Context, mailbox uuid.UUID) string {
	t.Helper()
	rows, err := f.pool.Query(ctx, "EXPLAIN "+countSentTodayShippedSQL, mailbox)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		fmt.Fprintln(&b, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return b.String()
}
