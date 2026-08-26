//go:build integration

package campaign

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

// The reported concentration read from real rows: the counters the rotation
// actually bumps, grouped by the addresses the mailboxes actually have. Docker must
// be up.

// moveTo puts a fixture mailbox on another address, which is how a two-domain pool
// is built — the shares group by the organizational domain of mailboxes.email.
func (f senderFixture) moveTo(t *testing.T, ctx context.Context, mailboxID uuid.UUID, email string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE mailboxes SET email = $1, smtp_username = $1, imap_username = $1
		  WHERE id = $2 AND workspace_id = $3`, email, mailboxID, f.ws); err != nil {
		t.Fatalf("move mailbox to %q: %v", email, err)
	}
}

// bump records n assignments against a pool member through the same statement the
// send path's claim transaction uses, so the reported usage is the usage rotation
// would have produced.
func (f senderFixture) bump(t *testing.T, ctx context.Context, mailboxID uuid.UUID, n int) {
	t.Helper()
	for range n {
		if err := f.q.BumpCampaignSenderAssignment(ctx, gen.BumpCampaignSenderAssignmentParams{
			CampaignID: f.campaignID, WorkspaceID: f.ws, MailboxID: mailboxID,
		}); err != nil {
			t.Fatalf("bump %s: %v", mailboxID, err)
		}
	}
}

// The usage half, end to end: seven contacts on one domain and three on another
// report 0.7/0.3, and the 0.7 is flagged against the limit that travels with it.
func TestGetSendersReportsConcentrationFromRealRotationCounters(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)
	f.moveTo(t, ctx, f.mailboxA, "a-"+uuid.NewString()+"@heavy.test")
	f.moveTo(t, ctx, f.mailboxB, "b-"+uuid.NewString()+"@light.test")
	svc := NewService(f.store, alwaysOKChecker{})

	if _, err := svc.SetSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 1, Enabled: true},
		{MailboxID: f.mailboxB, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("SetSenders: %v", err)
	}
	f.bump(t, ctx, f.mailboxA, 7)
	f.bump(t, ctx, f.mailboxB, 3)

	pool, err := svc.GetSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if pool.MaxFaultDomainShare != rotation.MaxFaultDomainShare {
		t.Errorf("limit = %v, want %v", pool.MaxFaultDomainShare, rotation.MaxFaultDomainShare)
	}
	if len(pool.FaultDomainShares) != 2 {
		t.Fatalf("shares = %+v, want two domains", pool.FaultDomainShares)
	}
	worst := pool.FaultDomainShares[0]
	if worst.Domain != "heavy.test" || worst.Assigned != 7 || worst.Share != 0.7 || !worst.OverBudget {
		t.Errorf("worst = %+v, want heavy.test 7 @ 0.7 over budget", worst)
	}
	if next := pool.FaultDomainShares[1]; next.Domain != "light.test" || next.Assigned != 3 || next.OverBudget {
		t.Errorf("second = %+v, want light.test 3 within budget", next)
	}
}

// A subdomain is not a second fault domain: mailboxes on mail.acme.test and
// acme.test fail together, and the panel must say so rather than showing two
// comfortable halves of one 100% exposure.
func TestReportedConcentrationGroupsSubdomainsWithTheirOrganizationalDomain(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)
	f.moveTo(t, ctx, f.mailboxA, "a-"+uuid.NewString()+"@acme.test")
	f.moveTo(t, ctx, f.mailboxB, "b-"+uuid.NewString()+"@mail.acme.test")
	svc := NewService(f.store, alwaysOKChecker{})

	if _, err := svc.SetSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 1, Enabled: true},
		{MailboxID: f.mailboxB, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("SetSenders: %v", err)
	}
	f.bump(t, ctx, f.mailboxA, 5)
	f.bump(t, ctx, f.mailboxB, 5)

	pool, err := svc.GetSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	if len(pool.FaultDomainShares) != 1 {
		t.Fatalf("shares = %+v, want ONE domain: a subdomain inherits its parent's fate",
			pool.FaultDomainShares)
	}
	got := pool.FaultDomainShares[0]
	if got.Domain != "acme.test" || got.Assigned != 10 || got.Share != 1 || !got.OverBudget {
		t.Errorf("share = %+v, want acme.test 10 @ 1.0 over budget", got)
	}
}

// A disabled member's history still counts. The risk lives in the mail already
// sent — a blocklisting damages every thread already running under that domain —
// so excluding it would under-report the exposure the moment an operator toggled a
// mailbox off.
func TestReportedConcentrationCountsDisabledMembersHistory(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)
	f.moveTo(t, ctx, f.mailboxA, "a-"+uuid.NewString()+"@heavy.test")
	f.moveTo(t, ctx, f.mailboxB, "b-"+uuid.NewString()+"@light.test")
	svc := NewService(f.store, alwaysOKChecker{})

	if _, err := svc.SetSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 1, Enabled: true},
		{MailboxID: f.mailboxB, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("SetSenders: %v", err)
	}
	f.bump(t, ctx, f.mailboxA, 9)
	f.bump(t, ctx, f.mailboxB, 1)
	// The replace preserves counters for a retained mailbox, so A keeps its 9.
	if _, err := svc.SetSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 1, Enabled: false},
		{MailboxID: f.mailboxB, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("second SetSenders: %v", err)
	}

	pool, err := svc.GetSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("GetSenders: %v", err)
	}
	worst := pool.FaultDomainShares[0]
	if worst.Domain != "heavy.test" || worst.Assigned != 9 || !worst.OverBudget {
		t.Errorf("worst = %+v, want heavy.test still at 9 and over budget after being disabled", worst)
	}
}

// The distribution is a tenant-scoped read like every other: a foreign workspace
// gets a not-found, never another tenant's exposure profile.
func TestReportedConcentrationIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	f := setupSenders(t, ctx)
	f.moveTo(t, ctx, f.mailboxA, "a-"+uuid.NewString()+"@heavy.test")
	svc := NewService(f.store, alwaysOKChecker{})
	if _, err := svc.SetSenders(ctx, f.ws, f.campaignID, rotation.ModeWeighted, []SenderInput{
		{MailboxID: f.mailboxA, Weight: 1, Enabled: true},
	}); err != nil {
		t.Fatalf("SetSenders: %v", err)
	}
	f.bump(t, ctx, f.mailboxA, 4)

	other, err := f.q.CreateWorkspace(ctx, "Intruder "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if _, err := svc.GetSenders(ctx, other.ID, f.campaignID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant GetSenders err = %v, want ErrNotFound", err)
	}
	// And the owner still sees its own, so this cannot pass by the read being broken.
	pool, err := svc.GetSenders(ctx, f.ws, f.campaignID)
	if err != nil {
		t.Fatalf("owner GetSenders: %v", err)
	}
	if len(pool.FaultDomainShares) != 1 || pool.FaultDomainShares[0].Domain != "heavy.test" {
		t.Errorf("owner's shares = %+v, want heavy.test", pool.FaultDomainShares)
	}
}
