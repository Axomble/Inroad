//go:build integration

package inprocess

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These tests exercise the new-lead-per-day throttle against Postgres, on the
// sender-pool fixture (setupPool, two steps). Docker must be up.

// setMaxNewLeads sets (or with nil clears) the campaign-wide new-lead-per-day
// limit.
func (f poolFixture) setMaxNewLeads(t *testing.T, ctx context.Context, limit *int32) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE campaigns SET max_new_leads_per_day = $2 WHERE id = $1`, f.campaignID, limit); err != nil {
		t.Fatalf("set max_new_leads_per_day: %v", err)
	}
}

// fillFirstStepsToday records `n` step-1 send rows today for the campaign, one
// per distinct contact, so CountFirstStepSendsToday sees them without needing a
// live claim/send round trip.
func (f poolFixture) fillFirstStepsToday(t *testing.T, ctx context.Context, n int) {
	t.Helper()
	for range n {
		c, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
			WorkspaceID: f.ws, Email: "newlead-" + uuid.NewString() + "@x.test", FirstName: "N",
		})
		if err != nil {
			t.Fatalf("filler contact: %v", err)
		}
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, step_order, status, sent_at)
			 VALUES ($1,$2,$3,$4,'filler@x.test',1,'sent', now())`,
			f.ws, f.campaignID, c.ID, f.mailboxA); err != nil {
			t.Fatalf("filler step-1 send: %v", err)
		}
	}
}

// The point of the whole feature: a step-1 job for a brand-new contact defers
// once the campaign has started as many new leads as its limit allows today,
// even though every mailbox in the pool has plenty of spare capacity.
func TestNewLeadLimitDefersAStep1JobThoughEveryMailboxHasCapacity(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	limit := int32(3)
	f.setMaxNewLeads(t, ctx, &limit)
	f.fillFirstStepsToday(t, ctx, 3)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob must defer, not error: %v", err)
	}
	if !job.NewLeadLimited {
		t.Fatalf("job = %+v, want NewLeadLimited", job)
	}
	if job.CampaignLimited {
		t.Error("the new-lead throttle must not also report CampaignLimited")
	}
	if job.Skip {
		t.Error("a campaign at its new-lead limit must defer, not skip")
	}
	// No mailbox was resolved for a send that will not happen.
	if job.SMTPPassword != nil || job.AccessToken != nil {
		t.Error("a new-lead-limited job opened a credential")
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Errorf("a new-lead-limited deferral pinned mailbox %s", got)
	}
	if status := f.enrollmentStatus(t, ctx, enrollmentID); status != "active" {
		t.Errorf("enrollment status = %q, want active", status)
	}

	// One under the limit and it sends again — the gate is a ceiling, not a latch.
	raised := int32(4)
	f.setMaxNewLeads(t, ctx, &raised)
	resumed, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if resumed.NewLeadLimited {
		t.Error("still NewLeadLimited below the raised limit")
	}
	if resumed.MailboxID == "" {
		t.Error("no mailbox resolved once the campaign was back under its new-lead limit")
	}
}

// The core invariant: a FOLLOW-UP step for a contact already mid-sequence must
// never be gated by the new-lead throttle, however exhausted today's new-lead
// allowance is. The throttle counts and limits only how many contacts START.
func TestNewLeadLimitNeverGatesAFollowUpStep(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	limit := int32(1)
	f.setMaxNewLeads(t, ctx, &limit)

	enrollmentID := f.enroll(t, ctx)
	// Step 1 sends and pins the mailbox, consuming the day's only new-lead slot.
	first, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("first step: %v", err)
	}
	if first.NewLeadLimited || first.MailboxID == "" {
		t.Fatalf("first step = %+v, want a normal send (the limit isn't hit yet)", first)
	}
	if _, err := f.core.ClaimStepSend(ctx, first); err != nil {
		t.Fatalf("claim step 1: %v", err)
	}
	if err := f.core.MarkStepDelivered(ctx, first, "<step1@acme.test>"); err != nil {
		t.Fatalf("deliver step 1: %v", err)
	}
	if _, err := f.core.AdvanceStepCursor(ctx, first); err != nil {
		t.Fatalf("advance cursor: %v", err)
	}

	// A second, unrelated contact would now be limited (the day's one slot is
	// used), proving the limit is genuinely in effect.
	other := f.enroll(t, ctx)
	otherJob, err := f.core.GetStepSendJob(ctx, other.String(), f.ws.String())
	if err != nil {
		t.Fatalf("other enrollment step: %v", err)
	}
	if !otherJob.NewLeadLimited {
		t.Fatalf("a second new contact = %+v, want NewLeadLimited once the day's one slot is used", otherJob)
	}

	// But THIS enrollment's follow-up (step 2) must proceed regardless.
	second, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("second step: %v", err)
	}
	if second.NewLeadLimited {
		t.Fatal("a follow-up step was gated by the new-lead-per-day throttle")
	}
	if second.StepOrder != 2 {
		t.Fatalf("step order = %d, want 2", second.StepOrder)
	}
	if second.MailboxID == "" {
		t.Error("the follow-up did not resolve a sender")
	}
}

// A NULL max_new_leads_per_day is exactly today's behaviour: unlimited new-lead
// starts, with no count taken.
func TestNullMaxNewLeadsPerDayBehavesAsBefore(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.setMaxNewLeads(t, ctx, nil)
	f.fillFirstStepsToday(t, ctx, 50) // well past any plausible limit

	job, err := f.core.GetStepSendJob(ctx, f.enroll(t, ctx).String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.NewLeadLimited {
		t.Error("NewLeadLimited with no limit configured")
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want A %s", job.MailboxID, f.mailboxA)
	}
}

// The count is workspace-pinned and campaign-scoped: another campaign's new
// leads, and another tenant's, must not consume this campaign's allowance.
func TestNewLeadLimitCountsOnlyItsOwnCampaign(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	limit := int32(2)
	f.setMaxNewLeads(t, ctx, &limit)

	// A second campaign in the same workspace, sharing the same mailbox, starts 5
	// new leads.
	lst, err := f.q.CreateList(ctx, gen.CreateListParams{WorkspaceID: f.ws, Name: "Other L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	other, err := f.q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: f.ws, Name: "Other", MailboxID: f.mailboxA, ListID: lst.ID,
		Subject: "Hi", BodyText: "Hello",
	})
	if err != nil {
		t.Fatalf("other campaign: %v", err)
	}
	for range 5 {
		c, cerr := f.q.UpsertContact(ctx, gen.UpsertContactParams{
			WorkspaceID: f.ws, Email: "other-" + uuid.NewString() + "@x.test", FirstName: "O",
		})
		if cerr != nil {
			t.Fatalf("contact: %v", cerr)
		}
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, step_order, status, sent_at)
			 VALUES ($1,$2,$3,$4,'other@x.test',1,'sent', now())`,
			f.ws, other.ID, c.ID, f.mailboxA); err != nil {
			t.Fatalf("other send: %v", err)
		}
	}

	job, err := f.core.GetStepSendJob(ctx, f.enroll(t, ctx).String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.NewLeadLimited {
		t.Error("another campaign's new leads consumed this campaign's new-lead limit")
	}
}
