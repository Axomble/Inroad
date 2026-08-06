//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

// These tests exercise ESP-matched sender selection against Postgres: the cache
// read on the send path, the fallbacks that keep matching an optimisation rather
// than a gate, and the sweep's list/record/evict seam. Docker must be up.

// sweepStaleWindow stands in for recipientesp.DefaultStaleAfter. Restated rather
// than imported: internal/worker may depend on internal/coreapi and never the
// other way round, and a test file does not get to invert that. Any positive
// window works here — every fixture row is brand new, so it is stale under all
// of them.
const sweepStaleWindow = 24 * time.Hour

// espFixtureCore restates recipientesp.Core for the same reason. It is the seam
// worker.Register reaches by type assertion, so a signature drift here would
// otherwise leave the sweep silently unregistered in production with the whole
// unit suite green.
type espFixtureCore interface {
	ListStaleRecipientDomains(ctx context.Context, staleAfter time.Duration) ([]coreapi.RecipientDomainRef, error)
	RecordRecipientDomainESP(ctx context.Context, in coreapi.RecipientDomainESP) error
	PurgeExpiredRecipientDomains(ctx context.Context, retention time.Duration) (int64, error)
}

// setSMTPHost rewrites a mailbox's submission host, which is what classifies a
// provider='smtp' mailbox — the fixture connects both mailboxes over SMTP, which
// is exactly the case `provider` alone gets wrong.
func (f poolFixture) setSMTPHost(t *testing.T, ctx context.Context, mailboxID uuid.UUID, host string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE mailboxes SET smtp_host = $1 WHERE id = $2 AND workspace_id = $3`,
		host, mailboxID, f.ws); err != nil {
		t.Fatalf("set smtp_host: %v", err)
	}
}

// enrollAt creates one contact at the given domain plus an active enrollment.
func (f poolFixture) enrollAt(t *testing.T, ctx context.Context, domain string) uuid.UUID {
	t.Helper()
	c, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "c-" + uuid.NewString() + "@" + domain, FirstName: "C",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	var id uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, next_due_at)
		 VALUES ($1,$2,$3, now()) RETURNING id`, f.ws, f.campaignID, c.ID).Scan(&id); err != nil {
		t.Fatalf("enrollment: %v", err)
	}
	return id
}

func (f poolFixture) espSweepCore(t *testing.T) espFixtureCore {
	t.Helper()
	c, ok := f.core.(espFixtureCore)
	if !ok {
		t.Fatal("the in-process client no longer satisfies recipientesp.Core; worker.Register's type assertion is now dead")
	}
	return c
}

// espFixture puts A (Google) and B (not) in the pool, weighted so that B wins
// every rotation mode unless the partition narrows the set first.
func espFixture(t *testing.T) (context.Context, poolFixture) {
	t.Helper()
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeWeighted)
	f.setSMTPHost(t, ctx, f.mailboxA, "smtp.gmail.com")
	f.setSMTPHost(t, ctx, f.mailboxB, "mail.selfhosted.test")
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.addSender(t, ctx, f.mailboxB, 100, true)
	return ctx, f
}

// The payoff: a Google recipient is assigned the Google mailbox even though the
// other member outranks it on the pool's own rotation rules.
func TestCachedRecipientESPRoutesToTheMatchingMailbox(t *testing.T) {
	ctx, f := espFixture(t)
	sweep := f.espSweepCore(t)
	if err := sweep.RecordRecipientDomainESP(ctx, coreapi.RecipientDomainESP{
		WorkspaceID: f.ws.String(), Domain: "googled.test", ESP: "google", MXHost: "aspmx.l.google.com",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	enrollmentID := f.enrollAt(t, ctx, "googled.test")
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want the Google-matched A %s", job.MailboxID, f.mailboxA)
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != f.mailboxA {
		t.Errorf("pinned = %s, want A %s", got, f.mailboxA)
	}
}

// A cache miss must neither block nor fail: the send resolves normally and the
// pool's own ranking decides. This is the invariant that lets the send path read
// the cache without ever touching DNS.
func TestUncachedRecipientDomainStillSends(t *testing.T) {
	ctx, f := espFixture(t)

	enrollmentID := f.enrollAt(t, ctx, "never-resolved.test")
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("a cache miss must not fail the send: %v", err)
	}
	if job.MailboxID != f.mailboxB.String() {
		t.Errorf("mailbox = %s, want the rotation winner B %s", job.MailboxID, f.mailboxB)
	}
}

// No mailbox in the pool is Microsoft, so a Microsoft recipient must fall back to
// the whole pool rather than defer. Matching is an optimisation, never a gate.
func TestUnmatchableRecipientFallsBackToTheFullPool(t *testing.T) {
	ctx, f := espFixture(t)
	sweep := f.espSweepCore(t)
	if err := sweep.RecordRecipientDomainESP(ctx, coreapi.RecipientDomainESP{
		WorkspaceID: f.ws.String(), Domain: "redmond.test", ESP: "microsoft",
		MXHost: "redmond.mail.protection.outlook.com",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	enrollmentID := f.enrollAt(t, ctx, "redmond.test")
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxB.String() {
		t.Errorf("mailbox = %s, want the rotation winner B %s", job.MailboxID, f.mailboxB)
	}
}

// Another workspace's cached answer must never steer this one's selection: the
// cache is workspace-pinned like every other tenant-scoped read.
func TestRecipientESPCacheIsWorkspacePinned(t *testing.T) {
	ctx, f := espFixture(t)
	sweep := f.espSweepCore(t)
	if err := sweep.RecordRecipientDomainESP(ctx, coreapi.RecipientDomainESP{
		WorkspaceID: f.foreignWS.String(), Domain: "cross.test", ESP: "google", MXHost: "aspmx.l.google.com",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	enrollmentID := f.enrollAt(t, ctx, "cross.test")
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxB.String() {
		t.Errorf("mailbox = %s, want B %s — a foreign workspace's cache row must not match", job.MailboxID, f.mailboxB)
	}
}

// The sweep's seam end to end: an enrolled-but-unpinned contact's domain is
// reported stale, a recorded answer takes it off the list, and eviction removes
// it again. The fan-out bound (active + unpinned only) is what keeps a table
// sized by the contact list from being filled by it.
func TestSweepSeamListsRecordsAndEvicts(t *testing.T) {
	ctx, f := espFixture(t)
	sweep := f.espSweepCore(t)
	enrollmentID := f.enrollAt(t, ctx, "sweepable.test")

	listed := func() bool {
		t.Helper()
		refs, err := sweep.ListStaleRecipientDomains(ctx, sweepStaleWindow)
		if err != nil {
			t.Fatalf("list stale: %v", err)
		}
		for _, r := range refs {
			if r.WorkspaceID == f.ws.String() && r.Domain == "sweepable.test" {
				return true
			}
		}
		return false
	}

	if !listed() {
		t.Fatal("an active unpinned enrollment's domain must be reported stale")
	}
	if err := sweep.RecordRecipientDomainESP(ctx, coreapi.RecipientDomainESP{
		WorkspaceID: f.ws.String(), Domain: "sweepable.test", ESP: "google", MXHost: "aspmx.l.google.com",
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if listed() {
		t.Error("a freshly classified domain must drop off the stale list")
	}

	// Pinning the enrollment takes it out of the fan-out for good: the sender is
	// write-once, so ESP matching can never apply to this thread again.
	if _, err := f.pool.Exec(ctx,
		`UPDATE sequence_enrollments SET mailbox_id = $1 WHERE id = $2`, f.mailboxA, enrollmentID); err != nil {
		t.Fatalf("pin: %v", err)
	}

	// Eviction with a zero retention window drops everything, including the row
	// just written — the mechanism the retention policy runs on.
	n, err := sweep.PurgeExpiredRecipientDomains(ctx, 0)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n < 1 {
		t.Errorf("purged %d rows, want at least the one just written", n)
	}
	if listed() {
		t.Error("a pinned enrollment's domain must not be swept: its sender can never change")
	}
}

// An incomplete lookup must never reach the table. Expressing it as a write would
// stamp checked_at and hide the domain from the next sweep for a full staleness
// window, on an answer that never arrived.
func TestRecordRejectsAnIncompleteLookup(t *testing.T) {
	ctx, f := espFixture(t)
	sweep := f.espSweepCore(t)
	f.enrollAt(t, ctx, "unresolved.test")

	for _, state := range []string{"unknown", "", "gmail"} {
		if err := sweep.RecordRecipientDomainESP(ctx, coreapi.RecipientDomainESP{
			WorkspaceID: f.ws.String(), Domain: "unresolved.test", ESP: state,
		}); err != nil {
			t.Fatalf("record(%q) must be a no-op, not an error: %v", state, err)
		}
	}
	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM recipient_domains WHERE workspace_id = $1 AND domain = 'unresolved.test'`,
		f.ws).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("%d rows written for an incomplete lookup, want 0", rows)
	}
}
