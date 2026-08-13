//go:build integration

package campaign

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These tests exercise the CAMPAIGN domain gate against Postgres: the aggregate
// "worst lane on this mailbox's sending domain" that ListCampaignSenders and
// GetCampaignFallbackSender derive, and which decides cap_today and the
// preflight's warmup_health verdict. Docker must be up.

// mailboxOn connects an active mailbox on an exact address in a workspace. The
// address matters: the gate groups mailboxes by their sending domain, so the
// domain has to be chosen by the test rather than by a fixture.
func mailboxOn(t *testing.T, ctx context.Context, q *gen.Queries, ws uuid.UUID, email string) uuid.UUID {
	t.Helper()
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws, Provider: "smtp", Email: email, DisplayName: email,
		SmtpHost: "smtp.x", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.x", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 100,
		MinIntervalSeconds: 0, RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox %s: %v", email, err)
	}
	return mb.ID
}

// quarantineMailbox makes a mailbox an ENABLED warmup participant in the
// quarantine lane — the state that withholds new campaign leads from every
// mailbox on its sending domain.
func quarantineMailbox(t *testing.T, ctx context.Context, f senderFixture, ws, mailboxID uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO warmup_participants (mailbox_id, workspace_id, lane, enabled)
		 VALUES ($1,$2,$3,true)
		 ON CONFLICT (mailbox_id) DO UPDATE SET lane = EXCLUDED.lane, enabled = true`,
		mailboxID, ws, warmup.LaneQuarantine); err != nil {
		t.Fatalf("quarantine %s: %v", mailboxID, err)
	}
}

// warmupHealthCheck pulls the one preflight verdict the domain gate drives.
func warmupHealthCheck(t *testing.T, report PreflightReport) PreflightCheck {
	t.Helper()
	for _, c := range report.Checks {
		if c.ID == CheckWarmupHealth {
			return c
		}
	}
	t.Fatalf("preflight has no %s check: %+v", CheckWarmupHealth, report.Checks)
	return PreflightCheck{}
}

// fixedCustomFields is the preflight loader's required custom-field reader. An
// unwired one deliberately fails the request (see WithCustomFields), and these
// fixtures have no {{custom.*}} tokens, so an empty key set says nothing either
// way about the check under test.
type fixedCustomFields struct{}

func (fixedCustomFields) CustomFieldKeys(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}

// security.md invariant 54: the campaign gate's domain scope is an aggregate read
// over the WORKSPACE's participants. Two tenants can legitimately send from the
// same domain — a shared parent company, a reseller, or simply the same public
// provider — and one tenant's containment must not be able to stop the other's
// campaigns. The SQL is correct by construction (the lateral pins
// wp2.workspace_id = cs.workspace_id) but nothing asserted it, so removing that
// predicate was a silent cross-tenant denial of service: quarantine one mailbox
// and every other tenant on the domain stops taking new leads, with the preflight
// naming a mailbox the operator cannot see, let alone fix.
//
// The same-workspace control at the end is what makes the first half meaningful:
// without it this test would also pass against a gate that had stopped working
// altogether.
func TestForeignQuarantineOnTheSameDomainWithholdsNothing(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)
	svc := NewService(f.store, alwaysOKChecker{}, WithCustomFields(fixedCustomFields{}))

	// One domain, one mailbox on it per tenant. A unique domain per run keeps a
	// shared test database's other fixtures out of the aggregate.
	domain := "pinned-" + uuid.NewString()[:8] + ".test"
	owner := mailboxOn(t, ctx, f.q, f.ws, "owner@"+domain)
	if err := f.store.ReplaceSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: owner, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("seed pool: %v", err)
	}

	intruderWS, err := f.q.CreateWorkspace(ctx, "Domain gate other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	intruder := mailboxOn(t, ctx, f.q, intruderWS.ID, "intruder@"+domain)
	quarantineMailbox(t, ctx, f, intruderWS.ID, intruder)

	senders, err := f.store.ListSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSenders: %v", err)
	}
	if len(senders) != 1 || senders[0].MailboxID != owner {
		t.Fatalf("pool = %+v, want only the owner mailbox", senders)
	}
	got := senders[0]
	if got.DomainLane != nil {
		t.Fatalf("domain_lane = %q, want none: another tenant's quarantine is not evidence about this domain",
			*got.DomainLane)
	}
	if !got.Sending || got.CapToday != 100 {
		t.Fatalf("sending=%v cap_today=%d, want true/100 — capacity was withheld by another tenant's mailbox",
			got.Sending, got.CapToday)
	}

	report, err := svc.Preflight(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if check := warmupHealthCheck(t, report); check.Severity == SeverityFail {
		t.Fatalf("preflight failed on another tenant's containment: %+v", check)
	}

	// Control: the identical lane, on the identical domain, INSIDE the workspace.
	// This one must bite — the sibling need not even be in the campaign's pool,
	// because reputation is assessed per domain, not per pool.
	sibling := mailboxOn(t, ctx, f.q, f.ws, "sibling@"+domain)
	quarantineMailbox(t, ctx, f, f.ws, sibling)

	senders, err = f.store.ListSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSenders after the same-workspace quarantine: %v", err)
	}
	contained := senders[0]
	if contained.DomainLane == nil || *contained.DomainLane != warmup.LaneQuarantine {
		t.Fatalf("domain_lane = %v, want quarantine: a same-workspace sibling on the domain must contain it",
			contained.DomainLane)
	}
	if contained.Sending || contained.CapToday != 0 {
		t.Fatalf("sending=%v cap_today=%d, want false/0 for a contained domain",
			contained.Sending, contained.CapToday)
	}
	report, err = svc.Preflight(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("Preflight after the same-workspace quarantine: %v", err)
	}
	if check := warmupHealthCheck(t, report); check.Severity != SeverityFail {
		t.Fatalf("preflight = %q, want fail once the domain is contained by its own tenant: %+v",
			check.Severity, check)
	}
}

// The gate's scope is the ORGANIZATIONAL domain, not the exact host. Providers
// largely inherit reputation across subdomains, so a quarantined
// sender@mail.<d>.test has almost certainly damaged the standing of
// owner@<d>.test — and expanding cold volume from the "clean" sibling spends a
// reputation that is already in trouble. Grouping on the exact host let exactly
// that happen.
func TestQuarantineOnASubdomainWithholdsTheParentDomain(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)

	parent := "org-" + uuid.NewString()[:8] + ".test"
	owner := mailboxOn(t, ctx, f.q, f.ws, "owner@"+parent)
	if err := f.store.ReplaceSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: owner, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("seed pool: %v", err)
	}
	// Same organization, different host.
	sibling := mailboxOn(t, ctx, f.q, f.ws, "sender@mail."+parent)
	quarantineMailbox(t, ctx, f, f.ws, sibling)

	senders, err := f.store.ListSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSenders: %v", err)
	}
	got := senders[0]
	if got.DomainLane == nil || *got.DomainLane != warmup.LaneQuarantine {
		t.Fatalf("domain_lane = %v, want quarantine: mail.%s and %s are one organizational domain",
			got.DomainLane, parent, parent)
	}
	if got.Sending || got.CapToday != 0 {
		t.Fatalf("sending=%v cap_today=%d, want false/0 — a subdomain's containment reaches the parent",
			got.Sending, got.CapToday)
	}

	// A DIFFERENT organization that merely ends in the same public suffix must
	// not be swept in: .test is a public suffix, so unrelated-<x>.test and
	// org-<x>.test are separate registrations.
	unrelated := mailboxOn(t, ctx, f.q, f.ws, "owner@unrelated-"+uuid.NewString()[:8]+".test")
	if err := f.store.ReplaceSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: unrelated, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("replace pool: %v", err)
	}
	senders, err = f.store.ListSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("ListSenders for the unrelated domain: %v", err)
	}
	if lane := senders[0].DomainLane; lane != nil {
		t.Fatalf("unrelated domain_lane = %q, want none: a shared public suffix is not a shared organization", *lane)
	}
	if !senders[0].Sending {
		t.Fatal("an unrelated domain was withheld by the quarantine")
	}
}

// The same pin on the OTHER read of the gate. A campaign with no pool rows sends
// from campaigns.mailbox_id, and that projection derives its own domain lane —
// the pool listing and the fallback are separate queries, so a predicate can be
// dropped from one and kept in the other.
func TestForeignQuarantineDoesNotWithholdTheFallbackSender(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)

	// The fixture's campaign already points at mailboxA and has no pool rows, so
	// mailboxA IS the fallback sender. Give another tenant a quarantined mailbox
	// on the same domain.
	a, err := f.q.GetMailbox(ctx, gen.GetMailboxParams{ID: f.mailboxA, WorkspaceID: f.ws})
	if err != nil {
		t.Fatalf("read mailbox A: %v", err)
	}
	at := strings.Index(a.Email, "@")
	if at < 0 {
		t.Fatalf("fixture mailbox %q has no domain to share", a.Email)
	}
	domain := a.Email[at+1:]
	intruderWS, err := f.q.CreateWorkspace(ctx, "Fallback gate other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	intruder := mailboxOn(t, ctx, f.q, intruderWS.ID, "intruder-"+uuid.NewString()+"@"+domain)
	quarantineMailbox(t, ctx, f, intruderWS.ID, intruder)

	fallback, err := f.store.FallbackSender(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("FallbackSender: %v", err)
	}
	if fallback.DomainLane != nil {
		t.Fatalf("fallback domain_lane = %q, want none: another tenant's quarantine must not reach it",
			*fallback.DomainLane)
	}
	if !fallback.Sending || fallback.CapToday != 100 {
		t.Fatalf("fallback sending=%v cap_today=%d, want true/100", fallback.Sending, fallback.CapToday)
	}

	// Control, same workspace, same domain.
	sibling := mailboxOn(t, ctx, f.q, f.ws, "sibling-"+uuid.NewString()+"@"+domain)
	quarantineMailbox(t, ctx, f, f.ws, sibling)
	contained, err := f.store.FallbackSender(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("FallbackSender after the same-workspace quarantine: %v", err)
	}
	if contained.DomainLane == nil || *contained.DomainLane != warmup.LaneQuarantine {
		t.Fatalf("fallback domain_lane = %v, want quarantine", contained.DomainLane)
	}
	if contained.Sending || contained.CapToday != 0 {
		t.Fatalf("fallback sending=%v cap_today=%d, want false/0", contained.Sending, contained.CapToday)
	}
}
