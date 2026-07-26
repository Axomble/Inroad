//go:build integration

package inprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// makeWarmupSend drives one A→B warmup send through its claim lifecycle and returns
// the finalized warmup_sends id — a real row a receipt can reference (the receipt's
// warmup_send_id FK requires it). B is always A's only same-workspace partner.
func makeWarmupSend(t *testing.T, ctx context.Context, f warmupFixture) (sendID, recipient string) {
	t.Helper()
	job, err := f.core.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil || job.Skip {
		t.Fatalf("GetWarmupSendJob: job=%+v err=%v", job, err)
	}
	if out, err := f.core.ClaimWarmupSend(ctx, job); err != nil || out != coreapi.ClaimWon {
		t.Fatalf("claim: out=%v err=%v, want Won", out, err)
	}
	if err := f.core.MarkWarmupSent(ctx, job, "<"+job.SendID+"@acme.test>"); err != nil {
		t.Fatalf("MarkWarmupSent: %v", err)
	}
	return job.SendID, job.ToMailbox
}

// todayStats sums a mailbox's daily_stats rows (a fresh fixture only has today's).
func todayStats(t *testing.T, ctx context.Context, f warmupFixture, mb uuid.UUID) (received, inbox, spam, replies int32) {
	t.Helper()
	rows, err := f.q.GetWarmupDailyStats(ctx, gen.GetWarmupDailyStatsParams{MailboxID: mb, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("GetWarmupDailyStats: %v", err)
	}
	for _, r := range rows {
		received += r.Received
		inbox += r.Inbox
		spam += r.Spam
		replies += r.Replies
	}
	return
}

// TestRecordWarmupReceiptIdempotent proves a re-poll of the SAME receipt returns a
// zero-value plan and never double-counts the placement stat.
func TestRecordWarmupReceiptIdempotent(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	in := coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID,
		RecipientMailbox: recipient, Placement: placementInbox,
	}
	plan1, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("first RecordWarmupReceipt: %v", err)
	}
	if plan1.ReceiptID == "" || !plan1.DoMarkRead {
		t.Fatalf("first plan = %+v, want a real plan (ReceiptID set, DoMarkRead)", plan1)
	}

	plan2, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("second RecordWarmupReceipt: %v", err)
	}
	if plan2 != (coreapi.WarmupEngagePlan{}) {
		t.Fatalf("duplicate plan = %+v, want zero value", plan2)
	}

	rb, _ := uuid.Parse(recipient)
	if received, _, _, _ := todayStats(t, ctx, f, rb); received != 1 {
		t.Fatalf("recipient received = %d, want 1 (no double count on re-poll)", received)
	}
}

// TestRecordWarmupReceiptPlacementStats proves inbox vs spam route to the right
// counter (each also bumps received) and spam sets DoRescue.
func TestRecordWarmupReceiptPlacementStats(t *testing.T) {
	ctx, f := setupWarmup(t)
	inboxSend, recipient := makeWarmupSend(t, ctx, f)
	spamSend, _ := makeWarmupSend(t, ctx, f)

	if _, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: inboxSend, RecipientMailbox: recipient, Placement: placementInbox,
	}); err != nil {
		t.Fatalf("inbox receipt: %v", err)
	}
	spamPlan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: spamSend, RecipientMailbox: recipient, Placement: placementSpam,
	})
	if err != nil {
		t.Fatalf("spam receipt: %v", err)
	}
	if !spamPlan.DoRescue {
		t.Fatalf("spam plan DoRescue = false, want true")
	}

	rb, _ := uuid.Parse(recipient)
	received, inbox, spam, _ := todayStats(t, ctx, f, rb)
	if received != 2 || inbox != 1 || spam != 1 {
		t.Fatalf("stats received=%d inbox=%d spam=%d, want 2/1/1", received, inbox, spam)
	}
}

// TestRecordWarmupReceiptCrossTenant proves the self-enforcing insert writes zero
// rows for a recipient outside the pinned workspace and fails closed.
func TestRecordWarmupReceiptCrossTenant(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, _ := makeWarmupSend(t, ctx, f)

	// Recipient C belongs to ws2; recording it under ws1 must write nothing.
	_, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: f.c.String(), Placement: placementInbox,
	})
	if !errors.Is(err, coreapi.ErrCrossTenant) {
		t.Fatalf("cross-tenant receipt err = %v, want ErrCrossTenant", err)
	}
}

// TestGetWarmupEngageJobLoadsTransport proves the engage job loads the recipient's
// decrypted transport and a source folder, and that MarkWarmupEngaged is idempotent.
func TestGetWarmupEngageJobAndMarkEngaged(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)
	plan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient, Placement: placementSpam,
	})
	if err != nil {
		t.Fatalf("RecordWarmupReceipt: %v", err)
	}

	job, err := f.core.GetWarmupEngageJob(ctx, plan.ReceiptID, f.ws1.String())
	if err != nil {
		t.Fatalf("GetWarmupEngageJob: %v", err)
	}
	if job.Provider != "smtp" || len(job.SMTPPassword) == 0 {
		t.Fatalf("engage job transport = %q pw-len=%d, want smtp with a decrypted password", job.Provider, len(job.SMTPPassword))
	}
	if job.SourceFolder != placementSpam || !job.DoRescue || !job.DoMarkRead {
		t.Fatalf("engage job = %+v, want spam source, rescue+markread", job)
	}

	// First mark engaged; when replied, the recipient's replies counter bumps once.
	if err := f.core.MarkWarmupEngaged(ctx, plan.ReceiptID, f.ws1.String(), true); err != nil {
		t.Fatalf("MarkWarmupEngaged: %v", err)
	}
	rb, _ := uuid.Parse(recipient)
	if _, _, _, replies := todayStats(t, ctx, f, rb); replies != 1 {
		t.Fatalf("replies = %d after first engage, want 1", replies)
	}
	// A retried engage is a no-op — no second reply count.
	if err := f.core.MarkWarmupEngaged(ctx, plan.ReceiptID, f.ws1.String(), true); err != nil {
		t.Fatalf("MarkWarmupEngaged (retry): %v", err)
	}
	if _, _, _, replies := todayStats(t, ctx, f, rb); replies != 1 {
		t.Fatalf("replies = %d after retried engage, want 1 (idempotent)", replies)
	}
}

// TestListDueWarmupMailboxes proves enabled non-paused participants are listed and a
// paused one is excluded (membership check — the fan-out is global across the shared DB).
func TestListDueWarmupMailboxes(t *testing.T) {
	ctx, f := setupWarmup(t)

	due, err := f.core.ListDueWarmupMailboxes(ctx)
	if err != nil {
		t.Fatalf("ListDueWarmupMailboxes: %v", err)
	}
	if !dueContains(due, f.a) || !dueContains(due, f.b) {
		t.Fatalf("due list missing A/B: %+v", due)
	}

	// Pause A with a live window; it must drop out of the due fan-out.
	if err := f.q.UpdateWarmupHealth(ctx, gen.UpdateWarmupHealthParams{
		MailboxID: f.a, WorkspaceID: f.ws1, HealthState: warmup.StatePaused, HealthReason: "test",
		PausedUntil: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("pause A: %v", err)
	}
	due2, err := f.core.ListDueWarmupMailboxes(ctx)
	if err != nil {
		t.Fatalf("ListDueWarmupMailboxes (after pause): %v", err)
	}
	if dueContains(due2, f.a) {
		t.Fatalf("paused A still in due list: %+v", due2)
	}
	if !dueContains(due2, f.b) {
		t.Fatalf("B dropped from due list: %+v", due2)
	}
}

func dueContains(refs []coreapi.MailboxRef, id uuid.UUID) bool {
	for _, r := range refs {
		if r.ID == id.String() {
			return true
		}
	}
	return false
}

// TestEvaluateWarmupHealthTransitionsAndRecovers proves EvaluateWarmupHealth
// escalates a spammy participant to paused and steps a clean paused participant one
// level back down (recovery), persisting both transitions.
func TestEvaluateWarmupHealthTransitionsAndRecovers(t *testing.T) {
	ctx, f := setupWarmup(t)

	// A: seed a spammy trailing window (6 spam / 4 inbox = 60% > 50% → paused).
	for i := 0; i < 6; i++ {
		if err := f.q.RecordWarmupReceiptStat(ctx, gen.RecordWarmupReceiptStatParams{MailboxID: f.a, WorkspaceID: f.ws1, Placement: placementSpam}); err != nil {
			t.Fatalf("seed A spam: %v", err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := f.q.RecordWarmupReceiptStat(ctx, gen.RecordWarmupReceiptStatParams{MailboxID: f.a, WorkspaceID: f.ws1, Placement: placementInbox}); err != nil {
			t.Fatalf("seed A inbox: %v", err)
		}
	}
	// B: currently paused but with a CLEAN window (no stats) → should recover one step.
	if err := f.q.UpdateWarmupHealth(ctx, gen.UpdateWarmupHealthParams{
		MailboxID: f.b, WorkspaceID: f.ws1, HealthState: warmup.StatePaused, HealthReason: "seed",
		PausedUntil: pgtype.Timestamptz{Time: time.Now().Add(72 * time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("seed B paused: %v", err)
	}

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	pa, err := f.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: f.a, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	if pa.HealthState != warmup.StatePaused {
		t.Fatalf("A health = %q, want paused (60%% spam)", pa.HealthState)
	}
	if !pa.PausedUntil.Valid {
		t.Fatalf("A paused_until not set on escalation")
	}

	pb, err := f.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: f.b, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if pb.HealthState != warmup.StateThrottled {
		t.Fatalf("B health = %q, want throttled (one-level recovery from paused)", pb.HealthState)
	}
}
