//go:build integration

package pulse

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fixture is one seeded workspace's ids, so assertions can reference what was
// planted.
type fixture struct {
	ws        gen.Workspace
	mailboxes []gen.Mailbox
}

// setup migrates and connects; each test seeds its own workspaces.
func setup(t *testing.T) (context.Context, *pgxpool.Pool, *gen.Queries) {
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
	return ctx, pool, gen.New(pool)
}

// addMailbox creates a mailbox with ramp disabled (so its cap is exactly
// dailyCap regardless of test-run age) and forces status/last_error, which
// CreateMailbox cannot set.
func addMailbox(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *gen.Queries, ws uuid.UUID, email, status, lastErr string, dailyCap int32) gen.Mailbox {
	t.Helper()
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: "T",
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: dailyCap,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox %s: %v", email, err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE mailboxes SET status = $1, last_error = $2 WHERE id = $3`, status, lastErr, mb.ID,
	); err != nil {
		t.Fatalf("mailbox %s status: %v", email, err)
	}
	return mb
}

// addWarmup enrolls a mailbox as an enabled warmup participant in the given
// health state.
func addWarmup(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, mb uuid.UUID, health string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO warmup_participants (mailbox_id, workspace_id, enabled, health_state)
		 VALUES ($1, $2, true, $3)`, mb, ws, health,
	); err != nil {
		t.Fatalf("warmup %s: %v", health, err)
	}
}

// seedFull plants a workspace exercising every aggregate and producer:
// mailbox statuses, warmup health mix, campaign statuses, contacts, sends
// inside/outside the UTC-day window, and a checked DMARC-less domain.
func seedFull(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *gen.Queries) fixture {
	t.Helper()
	ws, err := q.CreateWorkspace(ctx, "Pulse "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	// 2 active (one warming healthy, one throttled), 1 paused, 1 error.
	active1 := addMailbox(t, ctx, pool, q, ws.ID, "a1@pulse-a.test", "active", "", 100)
	active2 := addMailbox(t, ctx, pool, q, ws.ID, "a2@pulse-a.test", "active", "", 40)
	addMailbox(t, ctx, pool, q, ws.ID, "p1@pulse-a.test", "paused", "", 50)
	addMailbox(t, ctx, pool, q, ws.ID, "e1@pulse-a.test", "error", "auth failed", 50)
	addWarmup(t, ctx, pool, ws.ID, active1.ID, "healthy")
	addWarmup(t, ctx, pool, ws.ID, active2.ID, "throttled")

	// One list, campaigns in three statuses.
	var listID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO lists (workspace_id, name) VALUES ($1, 'L') RETURNING id`, ws.ID,
	).Scan(&listID); err != nil {
		t.Fatalf("list: %v", err)
	}
	var campaignID uuid.UUID
	for i, status := range []string{"running", "draft", "paused"} {
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO campaigns (workspace_id, name, mailbox_id, list_id, subject, status)
			 VALUES ($1, $2, $3, $4, 's', $5) RETURNING id`,
			ws.ID, fmt.Sprintf("C%d", i), active1.ID, listID, status,
		).Scan(&id); err != nil {
			t.Fatalf("campaign %s: %v", status, err)
		}
		if status == "running" {
			campaignID = id
		}
	}

	// One contact per send (the campaign+contact unique key forbids reuse):
	// 2 sent today, 1 sent yesterday (outside the UTC day window), 1 queued
	// (wrong status) — sent_today must be exactly 2, contacts.total 4.
	sendSpecs := []struct {
		status string
		sentAt string
	}{
		{"sent", "now()"},
		{"sent", "now()"},
		{"sent", "now() - interval '1 day'"},
		{"queued", "NULL"},
	}
	for i, spec := range sendSpecs {
		var contactID uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO contacts (workspace_id, email) VALUES ($1, $2) RETURNING id`,
			ws.ID, fmt.Sprintf("c%d@x.test", i),
		).Scan(&contactID); err != nil {
			t.Fatalf("contact %d: %v", i, err)
		}
		if _, err := pool.Exec(ctx,
			fmt.Sprintf(`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, sent_at)
			 VALUES ($1, $2, $3, $4, $5, $6, %s)`, spec.sentAt),
			ws.ID, campaignID, contactID, active1.ID, fmt.Sprintf("c%d@x.test", i), spec.status,
		); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	// The active senders' domain, checked, DMARC absent → dmarc_failing fires.
	if _, err := pool.Exec(ctx,
		`INSERT INTO sending_domains (workspace_id, domain, state, spf_found, dmarc_found, checked_at)
		 VALUES ($1, 'pulse-a.test', 'failing', true, false, now())`, ws.ID,
	); err != nil {
		t.Fatalf("sending domain: %v", err)
	}

	return fixture{ws: ws, mailboxes: []gen.Mailbox{active1, active2}}
}

// TestPulseAggregatesAndTenantIsolation seeds a fully-populated workspace A
// and a minimal workspace B, then asserts A's payload matches its fixtures
// exactly and NOTHING of A leaks into B's payload.
func TestPulseAggregatesAndTenantIsolation(t *testing.T) {
	ctx, pool, q := setup(t)
	svc := NewService(NewPgStore(q))

	a := seedFull(t, ctx, pool, q)

	// Workspace B: one active mailbox, one contact, nothing else.
	wsB, err := q.CreateWorkspace(ctx, "PulseB "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace B: %v", err)
	}
	addMailbox(t, ctx, pool, q, wsB.ID, "b1@pulse-b.test", "active", "", 25)
	if _, err := pool.Exec(ctx,
		`INSERT INTO contacts (workspace_id, email) VALUES ($1, 'bc@x.test')`, wsB.ID,
	); err != nil {
		t.Fatalf("contact B: %v", err)
	}

	pa, err := svc.Get(ctx, a.ws.ID)
	if err != nil {
		t.Fatalf("Get A: %v", err)
	}
	if pa.Mailboxes != (MailboxCounts{Total: 4, Active: 2, Paused: 1, Error: 1}) {
		t.Errorf("A mailboxes = %+v", pa.Mailboxes)
	}
	if pa.Warmup != (WarmupCounts{Pool: 2, Healthy: 1, Watch: 0, AtRisk: 1}) {
		t.Errorf("A warmup = %+v", pa.Warmup)
	}
	if pa.Campaigns != (CampaignCounts{Total: 3, Running: 1, Draft: 1, Paused: 1}) {
		t.Errorf("A campaigns = %+v", pa.Campaigns)
	}
	if pa.Contacts.Total != 4 {
		t.Errorf("A contacts = %+v, want 4", pa.Contacts)
	}
	// active1 healthy: 100; active2 throttled: 40/2 = 20. Paused/error
	// mailboxes contribute nothing.
	if pa.Sending != (SendingStatus{SentToday: 2, DailyCap: 120}) {
		t.Errorf("A sending = %+v, want {2 120}", pa.Sending)
	}
	if pa.Inbox != (InboxCounts{}) {
		t.Errorf("A inbox = %+v, want zeros", pa.Inbox)
	}

	wantAttention := []Attention{
		{Kind: "mailbox_error", Severity: "danger", Count: 1,
			Reason: "auth failed", Href: "/app/mailboxes?status=error"},
		{Kind: "senders_gated", Severity: "warn", Count: 1,
			Reason: "warmup health limiting sending: 1 throttled", Href: "/app/mailboxes"},
		{Kind: "dmarc_failing", Severity: "warn", Count: 1,
			Reason: "pulse-a.test has no verified DMARC record", Href: "/app/mailboxes"},
	}
	if len(pa.Attention) != len(wantAttention) {
		t.Fatalf("A attention = %+v, want %+v", pa.Attention, wantAttention)
	}
	for i, want := range wantAttention {
		if pa.Attention[i] != want {
			t.Errorf("A attention[%d] = %+v, want %+v", i, pa.Attention[i], want)
		}
	}

	// Workspace B sees ONLY its own rows: none of A's counts, sends, warmup,
	// or attention conditions bleed through.
	pb, err := svc.Get(ctx, wsB.ID)
	if err != nil {
		t.Fatalf("Get B: %v", err)
	}
	if pb.Mailboxes != (MailboxCounts{Total: 1, Active: 1}) {
		t.Errorf("B mailboxes = %+v, want its single active mailbox", pb.Mailboxes)
	}
	if pb.Warmup != (WarmupCounts{}) {
		t.Errorf("B warmup = %+v, want zeros", pb.Warmup)
	}
	if pb.Campaigns != (CampaignCounts{}) {
		t.Errorf("B campaigns = %+v, want zeros", pb.Campaigns)
	}
	if pb.Contacts.Total != 1 {
		t.Errorf("B contacts = %+v, want 1", pb.Contacts)
	}
	if pb.Sending != (SendingStatus{SentToday: 0, DailyCap: 25}) {
		t.Errorf("B sending = %+v, want {0 25}", pb.Sending)
	}
	if len(pb.Attention) != 0 {
		t.Errorf("B attention = %+v, want empty (A's conditions must not leak)", pb.Attention)
	}
}

// addInboxThread seeds one inbox_threads row directly (bypassing the
// enrollment-driven UpsertInboxThread path), so the test can hold the
// unread/last_reply_class combination fixed regardless of insert order. Each
// call uses a fresh root_message_id so the partial unique index never
// collides between rows.
func addInboxThread(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, mb uuid.UUID, unread bool, replyClass string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO inbox_threads (workspace_id, mailbox_id, root_message_id, last_reply_class, unread)
		 VALUES ($1, $2, $3, $4, $5)`,
		ws, mb, uuid.NewString(), replyClass, unread,
	); err != nil {
		t.Fatalf("inbox thread (unread=%v class=%q): %v", unread, replyClass, err)
	}
}

// TestPulseInboxCountsReflectUnreadAndInterested proves Inbox.Unread counts
// every unread thread and Inbox.Interested counts only the unread ones whose
// last reply classified positive — a READ positive-class thread must NOT
// count toward Interested (or Unread).
func TestPulseInboxCountsReflectUnreadAndInterested(t *testing.T) {
	ctx, pool, q := setup(t)
	ws, err := q.CreateWorkspace(ctx, "PulseInbox "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb := addMailbox(t, ctx, pool, q, ws.ID, "inbox@pulse-i.test", "active", "", 50)

	addInboxThread(t, ctx, pool, ws.ID, mb.ID, true, "positive")  // unread + interested
	addInboxThread(t, ctx, pool, ws.ID, mb.ID, true, "neutral")   // unread, not interested
	addInboxThread(t, ctx, pool, ws.ID, mb.ID, true, "")          // unread, not interested
	addInboxThread(t, ctx, pool, ws.ID, mb.ID, false, "positive") // read: must not count toward either

	p, err := NewService(NewPgStore(q)).Get(ctx, ws.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Inbox != (InboxCounts{Unread: 3, Interested: 1}) {
		t.Errorf("inbox = %+v, want {Unread:3 Interested:1}", p.Inbox)
	}
}

// TestPulseEmptyWorkspace proves a workspace with no rows anywhere returns
// the stable all-zero payload (no NULL-scan errors from the aggregate
// queries) and an empty, non-nil attention list.
func TestPulseEmptyWorkspace(t *testing.T) {
	ctx, _, q := setup(t)
	ws, err := q.CreateWorkspace(ctx, "PulseEmpty "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	p, err := NewService(NewPgStore(q)).Get(ctx, ws.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := Pulse{Attention: []Attention{}}
	if p.Mailboxes != want.Mailboxes || p.Warmup != want.Warmup || p.Campaigns != want.Campaigns ||
		p.Contacts != want.Contacts || p.Sending != want.Sending || p.Inbox != want.Inbox {
		t.Errorf("payload = %+v, want all zeros", p)
	}
	if p.Attention == nil || len(p.Attention) != 0 {
		t.Errorf("attention = %#v, want empty non-nil slice", p.Attention)
	}
}
