//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// seedDomainMailbox connects a mailbox on email in ws, so the domain is DERIVED
// (there is no way to create a sending domain other than sending from it).
func seedDomainMailbox(t *testing.T, ctx context.Context, q *gen.Queries, ws uuid.UUID, email string) {
	t.Helper()
	if _, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: "Domain IT",
		SmtpHost: "smtp.example.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.example.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 100, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	}); err != nil {
		t.Fatalf("mailbox %s: %v", email, err)
	}
}

func newWorkspace(t *testing.T, ctx context.Context, q *gen.Queries, name string) uuid.UUID {
	t.Helper()
	ws, err := q.CreateWorkspace(ctx, name+" "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return ws.ID
}

// staleDomains filters the global scan down to the workspace under test, so a
// developer database full of other fixtures cannot make the assertions flap.
func staleDomains(t *testing.T, ctx context.Context, c client, ws uuid.UUID, staleAfter time.Duration) []string {
	t.Helper()
	all, err := c.ListStaleSendingDomains(ctx, staleAfter)
	if err != nil {
		t.Fatalf("list stale: %v", err)
	}
	var out []string
	for _, d := range all {
		if d.WorkspaceID == ws.String() {
			out = append(out, d.Domain)
		}
	}
	return out
}

func TestSendingDomainsAreDerivedFromMailboxesAndSweepRespectsTheWindow(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := client{pool: pool, q: q}

	ws := newWorkspace(t, ctx, q, "Domain IT")
	suffix := uuid.NewString()[:8]
	shared := "shared-" + suffix + ".test"
	other := "other-" + suffix + ".test"
	// Two mailboxes on one domain: one row, one lookup — not one per mailbox.
	seedDomainMailbox(t, ctx, q, ws, "a@"+shared)
	seedDomainMailbox(t, ctx, q, ws, "b@"+shared)
	seedDomainMailbox(t, ctx, q, ws, "c@"+other)

	got := staleDomains(t, ctx, c, ws, 24*time.Hour)
	if len(got) != 2 || got[0] != other || got[1] != shared {
		t.Fatalf("stale = %v, want [%s %s] (never-checked domains, deduped)", got, other, shared)
	}

	if err := c.RecordSendingDomainAuth(ctx, coreapi.SendingDomainAuth{
		WorkspaceID: ws.String(), Domain: shared, State: "passing",
		SPFFound: true, SPFRecord: "v=spf1 -all",
		DMARCFound: true, DMARCPolicy: "reject",
		DKIMFound: true, DKIMSelector: "google",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Freshly checked, so the sweep stops offering it.
	if got := staleDomains(t, ctx, c, ws, 24*time.Hour); len(got) != 1 || got[0] != other {
		t.Fatalf("stale after check = %v, want [%s]", got, other)
	}
	// …until the window elapses. A zero window makes every completed check stale.
	if got := staleDomains(t, ctx, c, ws, 0); len(got) != 2 {
		t.Fatalf("stale with a zero window = %v, want both domains", got)
	}

	// Re-checking upserts the SAME row rather than inserting a second one.
	if err := c.RecordSendingDomainAuth(ctx, coreapi.SendingDomainAuth{
		WorkspaceID: ws.String(), Domain: shared, State: "failing",
		SPFFound: true, SPFRecord: "v=spf1 -all",
	}); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	var rows int
	var state, policy string
	if err := pool.QueryRow(ctx,
		`SELECT count(*), max(state), max(dmarc_policy) FROM sending_domains WHERE workspace_id = $1 AND domain = $2`,
		ws, shared).Scan(&rows, &state, &policy); err != nil {
		t.Fatalf("verify upsert: %v", err)
	}
	if rows != 1 || state != "failing" || policy != "" {
		t.Fatalf("after re-check: rows=%d state=%q policy=%q, want 1 failing with the cleared policy", rows, state, policy)
	}
}

// A transient lookup failure must not reach the table at all: it would both
// overwrite a known-good verdict and stamp checked_at, hiding the domain from
// the sweep for a full window.
func TestRecordSendingDomainAuthDropsUnknown(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := client{pool: pool, q: q}

	ws := newWorkspace(t, ctx, q, "Domain IT unknown")
	domain := "unknown-" + uuid.NewString()[:8] + ".test"
	seedDomainMailbox(t, ctx, q, ws, "a@"+domain)

	if err := c.RecordSendingDomainAuth(ctx, coreapi.SendingDomainAuth{
		WorkspaceID: ws.String(), Domain: domain, State: "passing",
		SPFFound: true, DMARCFound: true, DMARCPolicy: "reject",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := c.RecordSendingDomainAuth(ctx, coreapi.SendingDomainAuth{
		WorkspaceID: ws.String(), Domain: domain, State: "unknown",
	}); err != nil {
		t.Fatalf("record unknown: %v", err)
	}

	var state string
	var checkedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT state, checked_at FROM sending_domains WHERE workspace_id = $1 AND domain = $2`,
		ws, domain).Scan(&state, &checkedAt); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if state != "passing" || checkedAt == nil {
		t.Fatalf("state=%q checked_at=%v, want the known-good verdict untouched", state, checkedAt)
	}
	// And the domain stays stale, so the next sweep retries it.
	if got := staleDomains(t, ctx, c, ws, 0); len(got) != 1 || got[0] != domain {
		t.Fatalf("stale = %v, want [%s]", got, domain)
	}
}

// Two tenants sending from the same domain get their own rows and never read
// each other's verdict.
func TestSendingDomainsAreWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	c := client{pool: pool, q: q}

	domain := "tenant-" + uuid.NewString()[:8] + ".test"
	a := newWorkspace(t, ctx, q, "Domain IT A")
	b := newWorkspace(t, ctx, q, "Domain IT B")
	seedDomainMailbox(t, ctx, q, a, "a@"+domain)
	seedDomainMailbox(t, ctx, q, b, "b@"+domain)

	if err := c.RecordSendingDomainAuth(ctx, coreapi.SendingDomainAuth{
		WorkspaceID: a.String(), Domain: domain, State: "passing",
		SPFFound: true, DMARCFound: true, DMARCPolicy: "reject",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	// A's check does not answer for B: B is still unchecked, and still stale.
	if got := staleDomains(t, ctx, c, b, 24*time.Hour); len(got) != 1 || got[0] != domain {
		t.Fatalf("workspace B stale = %v, want [%s]", got, domain)
	}
	if got := staleDomains(t, ctx, c, a, 24*time.Hour); len(got) != 0 {
		t.Fatalf("workspace A stale = %v, want none", got)
	}

	rowsA, err := q.ListSendingDomains(ctx, a)
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	rowsB, err := q.ListSendingDomains(ctx, b)
	if err != nil {
		t.Fatalf("list B: %v", err)
	}
	stateOf := func(rows []gen.ListSendingDomainsRow) string {
		for _, r := range rows {
			if r.Domain == domain {
				return r.State
			}
		}
		return "<absent>"
	}
	if stateOf(rowsA) != "passing" || stateOf(rowsB) != "unknown" {
		t.Fatalf("A=%q B=%q, want passing/unknown — a verdict leaked across tenants",
			stateOf(rowsA), stateOf(rowsB))
	}
}
