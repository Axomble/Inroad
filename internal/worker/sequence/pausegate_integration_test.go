//go:build integration

package sequence

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// setCampaignStatus flips the campaign's status directly, which is what the
// deliverability circuit breaker's PauseCampaignForBreach does (guarded on
// status='running') and what a relaunch undoes.
func setCampaignStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, campaignID uuid.UUID, status string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE campaigns SET status = $3 WHERE id = $1 AND workspace_id = $2`,
		campaignID, ws, status); err != nil {
		t.Fatalf("set status %s: %v", status, err)
	}
}

func sendRowCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, campaignID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM sends WHERE workspace_id = $1 AND campaign_id = $2`,
		ws, campaignID).Scan(&n); err != nil {
		t.Fatalf("count sends: %v", err)
	}
	return n
}

func enrollmentState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, enrollmentID uuid.UUID) (status string, stopReason *string, currentStep int) {
	t.Helper()
	if err := pool.QueryRow(ctx,
		`SELECT status, stop_reason, current_step FROM sequence_enrollments WHERE id = $1`,
		enrollmentID).Scan(&status, &stopReason, &currentStep); err != nil {
		t.Fatalf("enrollment state: %v", err)
	}
	return status, stopReason, currentStep
}

// A campaign that is not running must not send, and its enrollments must SURVIVE
// to be resumed.
//
// This is the gate that makes the deliverability circuit breaker do anything at
// all. Before it, the breaker flipped campaigns.status to 'paused', wrote its
// pause event, and the campaign kept sending: nothing on the send path read that
// column, every mid-sequence enrollment is 'active' at the moment of the pause,
// and each successful send re-enqueues the next advance, so the chain is
// self-perpetuating.
//
// The test drives the REAL sequence:advance handler against real Postgres, and
// asserts both directions — a gate that never opened would pass the first half.
func TestPausedCampaignSendsNothingAndKeepsItsEnrollments(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()

	fx := seedCampaign(t, ctx, pool, q, newSealer(t), [][3]string{
		{"Step one", "First body", ""},
		{"Step two", "Second body", ""},
	})
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v ids=%d", err, len(ids))
	}
	enrollmentID := ids[0].ID

	// Step 1 goes out normally, so the enrollment is mid-sequence when the breaker
	// fires — the exact state the defect was invisible in, since a fresh enrollment
	// with nothing sent would also "not send" for uninteresting reasons.
	snd := &fakeSender{id: "<step1@x>"}
	advance(t, fx.core, snd, &fakeEnq{}, enrollmentID.String(), fx.ws.String())
	if snd.calls != 1 {
		t.Fatalf("step 1 did not send: %d calls", snd.calls)
	}
	if got := sendRowCount(t, ctx, pool, fx.ws, fx.campaignID); got != 1 {
		t.Fatalf("send rows after step 1 = %d, want 1", got)
	}
	status, _, step := enrollmentState(t, ctx, pool, enrollmentID)
	if status != "active" || step != 1 {
		t.Fatalf("enrollment after step 1 = %s at step %d, want active at 1", status, step)
	}

	// The breaker fires.
	setCampaignStatus(t, ctx, pool, fx.ws, fx.campaignID, "paused")

	pausedSender, pausedEnq := &fakeSender{id: "<must-not-send@x>"}, &fakeEnq{}
	advance(t, fx.core, pausedSender, pausedEnq, enrollmentID.String(), fx.ws.String())

	if pausedSender.calls != 0 {
		t.Errorf("a paused campaign SENT %d messages", pausedSender.calls)
	}
	if got := sendRowCount(t, ctx, pool, fx.ws, fx.campaignID); got != 1 {
		t.Errorf("send rows = %d, want the 1 from step 1 — a paused campaign created a send row", got)
	}
	// The enrollment must be waiting, not dead: an operator clears a pause by
	// relaunching, and enrollments marked 'failed' in the meantime would never
	// resume.
	status, reason, step := enrollmentState(t, ctx, pool, enrollmentID)
	if status != "active" {
		t.Errorf("enrollment status = %s (reason %v), want active — a pause must defer, not stop", status, reason)
	}
	if step != 1 {
		t.Errorf("current_step = %d, want 1 — a deferred step must not advance the cursor", step)
	}
	// And it must be re-queued, or nothing would ever pick it up again.
	if !pausedEnq.inCalled {
		t.Error("no re-enqueue for a paused campaign: the enrollment would be abandoned")
	}
	if pausedEnq.atCalled {
		t.Error("the next step was scheduled as if the send had happened")
	}

	// The converse: relaunching resumes the sequence. Without this half, a gate
	// stuck permanently closed would pass every assertion above.
	setCampaignStatus(t, ctx, pool, fx.ws, fx.campaignID, "running")
	resumed := &fakeSender{id: "<step2@x>"}
	advance(t, fx.core, resumed, &fakeEnq{}, enrollmentID.String(), fx.ws.String())
	if resumed.calls != 1 {
		t.Fatalf("relaunched campaign did not resume: %d calls", resumed.calls)
	}
	if got := sendRowCount(t, ctx, pool, fx.ws, fx.campaignID); got != 2 {
		t.Errorf("send rows after resuming = %d, want 2", got)
	}
	if _, _, step := enrollmentState(t, ctx, pool, enrollmentID); step != 2 {
		t.Errorf("current_step = %d after resuming, want 2", step)
	}
}

// The gate is on "is it running", not "is it paused": draft and done must not send
// either. A denylist of the states that may not send would let a new state (or a
// campaign that was never launched) through by default.
func TestOnlyARunningCampaignSends(t *testing.T) {
	for _, status := range []string{"draft", "paused", "done"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			pool, q, closeFn := connect(t)
			defer closeFn()

			fx := seedCampaign(t, ctx, pool, q, newSealer(t), [][3]string{{"S", "B", ""}})
			ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
			if err != nil || len(ids) != 1 {
				t.Fatalf("enroll: %v ids=%d", err, len(ids))
			}
			setCampaignStatus(t, ctx, pool, fx.ws, fx.campaignID, status)

			snd, enq := &fakeSender{id: "<nope@x>"}, &fakeEnq{}
			advance(t, fx.core, snd, enq, ids[0].ID.String(), fx.ws.String())

			if snd.calls != 0 {
				t.Errorf("a %s campaign sent %d messages", status, snd.calls)
			}
			if got := sendRowCount(t, ctx, pool, fx.ws, fx.campaignID); got != 0 {
				t.Errorf("a %s campaign created %d send rows", status, got)
			}
			if s, _, _ := enrollmentState(t, ctx, pool, ids[0].ID); s != "active" {
				t.Errorf("enrollment status = %s for a %s campaign, want active", s, status)
			}
		})
	}
}

// A paused campaign must not unseal a credential or pin a mailbox for a send that
// will not happen. The gate sits before both, so the job comes back with no
// transport at all and the enrollment's mailbox_id is untouched.
func TestPausedCampaignResolvesNoSenderAndNoCredential(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()

	fx := seedCampaign(t, ctx, pool, q, newSealer(t), [][3]string{{"S", "B", ""}})
	ids, err := q.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil || len(ids) != 1 {
		t.Fatalf("enroll: %v ids=%d", err, len(ids))
	}
	setCampaignStatus(t, ctx, pool, fx.ws, fx.campaignID, "paused")

	job, err := fx.core.GetStepSendJob(ctx, ids[0].ID.String(), fx.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if !job.CampaignPaused {
		t.Fatalf("CampaignPaused = false for a paused campaign (job %+v)", job)
	}
	if job.Skip {
		t.Error("Skip = true: a pause is a deferral, not 'nothing to do ever'")
	}
	if len(job.SMTPPassword) != 0 || len(job.AccessToken) != 0 {
		t.Error("a paused campaign unsealed a credential it will not use")
	}
	if job.MailboxID != "" || job.SMTPHost != "" {
		t.Errorf("a paused campaign resolved a sender: mailbox=%q host=%q", job.MailboxID, job.SMTPHost)
	}
	// The schedule DOES travel, because the deferred retry sends as soon as it runs
	// and has to wake inside the campaign's window.
	if len(job.Schedule.Windows) == 0 {
		t.Error("no schedule on the deferred job: the retry could wake outside the send window")
	}
	var pinned *uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT mailbox_id FROM sequence_enrollments WHERE id = $1`, ids[0].ID).Scan(&pinned); err != nil {
		t.Fatalf("pinned mailbox: %v", err)
	}
	if pinned != nil {
		t.Errorf("a paused campaign pinned mailbox %s to the thread", pinned)
	}
}
