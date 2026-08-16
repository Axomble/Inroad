//go:build integration

package inprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// TestRecordWarmupReceiptUnengagedDuplicateSelfHeals proves the self-heal: a re-poll of
// the SAME still-UNENGAGED receipt re-returns the EXACT same deterministic plan (so a
// poller that lost the warmup:engage enqueue after the first commit re-enqueues it) and
// still never double-counts the placement/received stat (the tx rolled back, no stat
// re-written).
func TestRecordWarmupReceiptUnengagedDuplicateSelfHeals(t *testing.T) {
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

	// The duplicate is still unengaged (C5b never ran), so the engage may have been
	// lost — the plan is re-returned so the poller re-enqueues.
	before := time.Now().UTC()
	plan2, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("second RecordWarmupReceipt: %v", err)
	}
	assertSelfHealedPlan(t, plan1, plan2, before)

	rb, _ := uuid.Parse(recipient)
	if received, _, _, _ := todayStats(t, ctx, f, rb); received != 1 {
		t.Fatalf("recipient received = %d, want 1 (self-heal must not re-write stats)", received)
	}
}

// TestRecordWarmupReceiptSpamDuplicateSelfHeals proves the junk-path counterpart: a
// re-scan of a still-unengaged SPAM receipt re-returns the same rescue plan (ReceiptID
// set, DoRescue true) so the poller re-enqueues the lost engage — with no stat
// double-count.
func TestRecordWarmupReceiptSpamDuplicateSelfHeals(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	in := coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID,
		RecipientMailbox: recipient, Placement: placementSpam,
	}
	plan1, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("first RecordWarmupReceipt: %v", err)
	}
	if plan1.ReceiptID == "" || !plan1.DoRescue {
		t.Fatalf("first spam plan = %+v, want ReceiptID set + DoRescue", plan1)
	}

	before := time.Now().UTC()
	plan2, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("second RecordWarmupReceipt: %v", err)
	}
	assertSelfHealedPlan(t, plan1, plan2, before)
}

// assertSelfHealedPlan checks that a plan rebuilt for a still-unengaged DUPLICATE
// receipt matches the original. Every DECISION must be re-derived identically from the
// stored row — that IS the self-heal, and it is what lets the poller safely re-enqueue.
//
// EngageAfter is compared as a target instant rather than for equality, because it is a
// delay relative to NOW and is deliberately re-derived on every call: a plan rebuilt
// long after the receipt fires at the next waking instant instead of waiting out the
// full reply latency a second time. Two calls a moment apart therefore return delays
// that differ by that moment. `before` is a timestamp taken just before the second
// call, bounding how much shrinkage is legitimate.
func assertSelfHealedPlan(t *testing.T, plan1, plan2 coreapi.WarmupEngagePlan, before time.Time) {
	t.Helper()

	if plan2.ReceiptID != plan1.ReceiptID || plan2.DoRescue != plan1.DoRescue ||
		plan2.DoMarkRead != plan1.DoMarkRead || plan2.DoReply != plan1.DoReply {
		t.Fatalf("duplicate plan = %+v, want the same decisions as the first (%+v)", plan2, plan1)
	}
	// Tolerance: the wall-clock time the two calls were separated by, plus slack for
	// scheduling. The delay may only shrink, never grow.
	tolerance := time.Since(before) + time.Second
	if diff := plan1.EngageAfter - plan2.EngageAfter; diff < 0 || diff > tolerance {
		t.Fatalf("EngageAfter drifted by %v between calls (tolerance %v): %v then %v",
			diff, tolerance, plan1.EngageAfter, plan2.EngageAfter)
	}
}

// TestRecordWarmupReceiptEngagedDuplicateReturnsEmpty proves the guard that keeps the
// self-heal from double-engaging: once the receipt is marked engaged (C5b ran), a
// re-poll returns the ZERO-value plan (nothing to re-enqueue) and never re-writes stats.
func TestRecordWarmupReceiptEngagedDuplicateReturnsEmpty(t *testing.T) {
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
	// Engage it (C5b): flips engaged=true.
	if err := f.core.MarkWarmupEngaged(ctx, plan1.ReceiptID, f.ws1.String(), false); err != nil {
		t.Fatalf("MarkWarmupEngaged: %v", err)
	}

	plan2, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("second RecordWarmupReceipt: %v", err)
	}
	if plan2 != (coreapi.WarmupEngagePlan{}) {
		t.Fatalf("engaged duplicate plan = %+v, want zero value (no re-enqueue)", plan2)
	}

	rb, _ := uuid.Parse(recipient)
	if received, _, _, _ := todayStats(t, ctx, f, rb); received != 1 {
		t.Fatalf("recipient received = %d, want 1 (no double count)", received)
	}
}

// TestRecordWarmupReceiptDuplicateNonParticipantReturnsEmpty proves the self-heal takes
// the same clean skip the fresh path does when the recipient is no longer a warmup
// participant: an unengaged duplicate whose participant row is gone returns the empty
// plan (no re-enqueue), rather than failing.
func TestRecordWarmupReceiptDuplicateNonParticipantReturnsEmpty(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)
	rb, err := uuid.Parse(recipient)
	if err != nil {
		t.Fatalf("parse recipient: %v", err)
	}

	in := coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID,
		RecipientMailbox: recipient, Placement: placementInbox,
	}
	if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
		t.Fatalf("first RecordWarmupReceipt: %v", err)
	}
	// Drop the recipient's participant row: still a real workspace mailbox, no longer a
	// warmup participant.
	if _, err := f.q.DisableWarmupParticipant(ctx, gen.DisableWarmupParticipantParams{MailboxID: rb, WorkspaceID: f.ws1}); err != nil {
		t.Fatalf("disable participant: %v", err)
	}

	plan2, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("duplicate non-participant RecordWarmupReceipt err = %v, want nil (clean skip)", err)
	}
	if plan2 != (coreapi.WarmupEngagePlan{}) {
		t.Fatalf("duplicate non-participant plan = %+v, want zero value", plan2)
	}
}

// TestRecordWarmupReceiptSenderAttribution proves placement is attributed to the
// SENDER, not the recipient (spec §4): when recipient B records A's mail as inbox
// then spam, A's (sender) inbox/spam counters increment and B's (recipient) received
// counter increments — B's spam/inbox stay ZERO (the innocent inbox owner is never
// punished for what it received). spam still sets DoRescue on the recipient's plan.
func TestRecordWarmupReceiptSenderAttribution(t *testing.T) {
	ctx, f := setupWarmup(t)
	inboxSend, recipient := makeWarmupSend(t, ctx, f) // A -> B, from_mailbox = A
	spamSend, _ := makeWarmupSend(t, ctx, f)          // A -> B

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

	// Recipient B: observes the mail (received bumps) but placement is NEVER attributed
	// to it — inbox/spam must stay 0.
	rb, _ := uuid.Parse(recipient)
	received, rInbox, rSpam, _ := todayStats(t, ctx, f, rb)
	if received != 2 || rInbox != 0 || rSpam != 0 {
		t.Fatalf("recipient B stats received=%d inbox=%d spam=%d, want 2/0/0 (recipient observes, never attributed placement)", received, rInbox, rSpam)
	}

	// Sender A: the deliverability signal lands here — one inbox, one spam.
	_, aInbox, aSpam, _ := todayStats(t, ctx, f, f.a)
	if aInbox != 1 || aSpam != 1 {
		t.Fatalf("sender A placement inbox=%d spam=%d, want 1/1 (placement attributed to sender)", aInbox, aSpam)
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

func TestRecordWarmupReceiptRejectsWrongSameWorkspaceRecipient(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, _ := makeWarmupSend(t, ctx, f) // A sent to B.

	plan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID,
		RecipientMailbox: f.a.String(), Placement: placementInbox,
	})
	if err != nil {
		t.Fatalf("wrong same-workspace recipient: %v", err)
	}
	if plan.ReceiptID != "" {
		t.Fatalf("wrong recipient returned engagement plan: %+v", plan)
	}
	_, inbox, spam, _ := todayStats(t, ctx, f, f.a)
	if inbox != 0 || spam != 0 {
		t.Fatalf("wrong recipient changed sender placement: inbox=%d spam=%d", inbox, spam)
	}
}

// TestGetWarmupEngageJobLoadsTransport proves the engage job loads the recipient's
// decrypted transport and a source folder, and that MarkWarmupEngaged is idempotent.
func TestGetWarmupEngageJobAndMarkEngaged(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)
	plan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient, Placement: placementSpam,
		SourceFolder: "Junk", MessageID: "<orig@acme.test>",
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
	// The engager locates the message by the receipt's ACTUAL folder + Message-ID, and
	// dials the recipient's IMAP-MODIFY transport (loaded from the mailbox).
	if job.SourceFolder != "Junk" || job.MessageID != "<orig@acme.test>" {
		t.Fatalf("engage locator = %q/%q, want Junk/<orig@acme.test>", job.SourceFolder, job.MessageID)
	}
	if job.IMAPHost != "imap.acme.test" || job.IMAPPort != 993 {
		t.Fatalf("engage IMAP transport = %q:%d, want imap.acme.test:993", job.IMAPHost, job.IMAPPort)
	}
	if !job.DoRescue || !job.DoMarkRead {
		t.Fatalf("engage job = %+v, want rescue+markread on a spam placement", job)
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

// TestWarmupReplySendIDStableAcrossRetry is the MAJOR reply-idempotency guard: the
// engagement reply's warmup_sends SendID is anchored to the IMMUTABLE receipt id, so a
// post-send engage retry of GetWarmupEngageJob re-derives the IDENTICAL reply SendID —
// even after the recipient's sent-today counter has advanced (the reply's own
// MarkWarmupSent bump, plus later tick sends). Under the OLD (recipient, day, sentToday)
// derivation the id would DRIFT on retry, ClaimWarmupSend would INSERT a fresh row, win
// the claim, and the reply would be SENT TWICE (corrupting content + the replies/sent
// counters). With the receipt anchor the retry reclaims the same row (ClaimAlreadySent).
func TestWarmupReplySendIDStableAcrossRetry(t *testing.T) {
	ctx, f := setupWarmup(t)
	// Force recipient B to ALWAYS reply (reply_rate=1.0 ⇒ ReplyDecision true) so
	// GetWarmupEngageJob deterministically builds the reply send under test.
	if _, err := f.q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
		MailboxID: f.b, WorkspaceID: f.ws1, StartVolume: 8, MaxVolume: 40, RampIncrement: 2, ReplyRate: 1.0,
	}); err != nil {
		t.Fatalf("force reply_rate=1: %v", err)
	}
	sendID, recipient := makeWarmupSend(t, ctx, f) // recipient == B
	plan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementInbox, SourceFolder: "INBOX", MessageID: "<orig@acme.test>",
	})
	if err != nil {
		t.Fatalf("RecordWarmupReceipt: %v", err)
	}

	job1, err := f.core.GetWarmupEngageJob(ctx, plan.ReceiptID, f.ws1.String())
	if err != nil {
		t.Fatalf("GetWarmupEngageJob #1: %v", err)
	}
	if !job1.DoReply || job1.ReplySend.SendID == "" {
		t.Fatalf("engage job #1 = %+v, want a built reply carrying a SendID", job1)
	}
	id1 := job1.ReplySend.SendID

	// Simulate the exact post-send state the double-send bug hinged on: the reply's own
	// MarkWarmupSent → IncrementWarmupSentStat (and any later tick send) advances the
	// recipient's sent-today counter that the OLD derivation summed.
	rb, err := uuid.Parse(recipient)
	if err != nil {
		t.Fatalf("parse recipient: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := f.q.IncrementWarmupSentStat(ctx, gen.IncrementWarmupSentStatParams{MailboxID: rb, WorkspaceID: f.ws1}); err != nil {
			t.Fatalf("advance sent-today: %v", err)
		}
	}

	job2, err := f.core.GetWarmupEngageJob(ctx, plan.ReceiptID, f.ws1.String())
	if err != nil {
		t.Fatalf("GetWarmupEngageJob #2 (retry): %v", err)
	}
	if !job2.DoReply || job2.ReplySend.SendID == "" {
		t.Fatalf("engage job #2 = %+v, want the reply still built on retry", job2)
	}
	if job2.ReplySend.SendID != id1 {
		t.Fatalf("reply SendID DRIFTED across retry: #1=%s #2=%s — ClaimWarmupSend would insert a fresh row and re-send", id1, job2.ReplySend.SendID)
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
// escalates a spammy SENDER to paused (placement is sender-attributed), steps a
// clean paused participant whose timed block has ELAPSED one level back down
// (recovery), and — the timed-block floor — does NOT recover a clean participant
// whose paused_until is still in the future.
func TestEvaluateWarmupHealthTransitionsAndRespectsEvidence(t *testing.T) {
	ctx, f := setupWarmup(t)

	// A: seed a spammy trailing window attributed to A as the SENDER via a real A->B
	// send (6 spam / 4 inbox = 60% > 50% → paused). RecordWarmupSenderPlacementStat
	// resolves the sender from warmup_sends.from_mailbox = A.
	sendID, _ := makeWarmupSend(t, ctx, f)
	sid, err := uuid.Parse(sendID)
	if err != nil {
		t.Fatalf("parse sendID: %v", err)
	}
	for i := 0; i < 20; i++ {
		placement := placementInbox
		if i < 12 {
			placement = placementSpam
		}
		if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
			WorkspaceID: f.ws1, WarmupSendID: sid, RecipientMailbox: f.b,
			ReceiptID: uuid.New(), Placement: placement,
			ObservedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}); err != nil {
			t.Fatalf("seed A spam: %v", err)
		}
	}
	// B: paused with a CLEAN window whose block has ELAPSED (paused_until in the past)
	// → should recover one step (paused → throttled).
	if err := f.q.UpdateWarmupHealth(ctx, gen.UpdateWarmupHealthParams{
		MailboxID: f.b, WorkspaceID: f.ws1, HealthState: warmup.StatePaused, HealthReason: "seed",
		PausedUntil: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("seed B paused: %v", err)
	}
	// C (ws2): paused with a CLEAN window but the block is STILL LIVE (paused_until in
	// the future) → the timed-block floor must hold it paused (no recovery yet).
	if err := f.q.UpdateWarmupHealth(ctx, gen.UpdateWarmupHealthParams{
		MailboxID: f.c, WorkspaceID: f.ws2, HealthState: warmup.StatePaused, HealthReason: "seed",
		PausedUntil: pgtype.Timestamptz{Time: time.Now().Add(72 * time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("seed C paused: %v", err)
	}

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	pa, err := f.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: f.a, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	// 12 spam of 20 is 60% observed, but the policy compares a Wilson 95% LOWER
	// bound, which for that sample is ~38.7% — over the 30% throttle band, under the
	// 50% pause band. Deliberate: 20 observations cannot establish a 50% rate with
	// confidence, and Phase 0's false positives came from treating thin samples as
	// certain. What matters here is that sender-attributed spam degrades A at all.
	if pa.HealthState != warmup.StateThrottled {
		t.Fatalf("A health = %q, want throttled (60%% observed sender-attributed spam bounds to ~38.7%%)", pa.HealthState)
	}
	if !pa.PausedUntil.Valid {
		t.Fatalf("A paused_until not set on escalation")
	}

	pb, err := f.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: f.b, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if pb.HealthState != warmup.StatePaused {
		t.Fatalf("B health = %q, want paused (recovery requires qualified placement evidence)", pb.HealthState)
	}

	pc, err := f.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: f.c, WorkspaceID: f.ws2})
	if err != nil {
		t.Fatalf("read C: %v", err)
	}
	if pc.HealthState != warmup.StatePaused {
		t.Fatalf("C health = %q, want paused (timed-block floor: no recovery while paused_until is live)", pc.HealthState)
	}
}

// TestRecordWarmupReceiptNonParticipantSkip proves a receipt for a recipient that is
// NOT a warmup participant is a clean no-op skip (spec §4): an empty plan, nil error,
// and NO receipt or stat persisted (the tx rolls back).
func TestRecordWarmupReceiptNonParticipantSkip(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f) // A -> B
	rb, err := uuid.Parse(recipient)
	if err != nil {
		t.Fatalf("parse recipient: %v", err)
	}

	// Drop B's participant row so the recipient is a real workspace mailbox but no
	// longer a warmup participant.
	if _, err := f.q.DisableWarmupParticipant(ctx, gen.DisableWarmupParticipantParams{MailboxID: rb, WorkspaceID: f.ws1}); err != nil {
		t.Fatalf("disable B participant: %v", err)
	}

	plan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient, Placement: placementSpam,
	})
	if err != nil {
		t.Fatalf("non-participant RecordWarmupReceipt err = %v, want nil (clean skip)", err)
	}
	if plan != (coreapi.WarmupEngagePlan{}) {
		t.Fatalf("non-participant plan = %+v, want zero value", plan)
	}

	// No stat persisted for the non-participant recipient, and no receipt row (so a
	// later re-poll after re-enabling would still record it fresh).
	if received, _, _, _ := todayStats(t, ctx, f, rb); received != 0 {
		t.Fatalf("recipient received = %d after non-participant skip, want 0 (nothing persisted)", received)
	}
	if _, err := f.q.GetWarmupReceiptByPair(ctx, gen.GetWarmupReceiptByPairParams{
		WarmupSendID: pgtype.UUID{Bytes: sid16(t, sendID), Valid: true}, RecipientMailbox: rb, WorkspaceID: f.ws1,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetWarmupReceiptByPair err = %v, want ErrNoRows (no receipt persisted)", err)
	}
}

// sid16 parses a send-id string to the raw 16 bytes for a pgtype.UUID.
func sid16(t *testing.T, s string) [16]byte {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("parse send id: %v", err)
	}
	return u
}

// A mailbox the deploy migration demoted to `unknown` must be able to earn its way
// back once it has qualified evidence. `unknown` shares healthy's rank (it is an
// absence of evidence, not a degree of badness), so the evaluator's recovery-step
// branch does not fire for unknown → healthy and the decision carried no reason
// code — which warmup_state_transitions rejects, rolling back the participant
// update in the same atomic statement. The mailbox then re-failed every sweep and
// stayed unknown forever at half its cold-send cap. Asserts the promotion lands AND
// that it is explained, because the missing explanation is what broke the write.
func TestEvaluateWarmupHealthPromotesFromUnknownWithQualifiedEvidence(t *testing.T) {
	ctx, f := setupWarmup(t)

	sid := warmupSendUUID(t, ctx, f)
	for i := 0; i < 20; i++ {
		if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
			WorkspaceID: f.ws1, WarmupSendID: sid, RecipientMailbox: f.b,
			ReceiptID: uuid.New(), Placement: placementInbox,
			ObservedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		}); err != nil {
			t.Fatalf("seed clean placement %d: %v", i, err)
		}
	}
	if err := f.q.UpdateWarmupHealth(ctx, gen.UpdateWarmupHealthParams{
		MailboxID: f.a, WorkspaceID: f.ws1, HealthState: warmup.StateUnknown,
		HealthReason: "not enough recent placement evidence",
	}); err != nil {
		t.Fatalf("seed A unknown: %v", err)
	}

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	pa, err := f.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: f.a, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	if pa.HealthState != warmup.StateHealthy {
		t.Fatalf("A health = %q, want healthy (20 clean placements qualify it)", pa.HealthState)
	}

	var reasonCode string
	if err := f.raw.QueryRow(ctx,
		`SELECT reason_code FROM warmup_state_transitions
		 WHERE workspace_id = $1 AND mailbox_id = $2 AND from_state = 'unknown' AND to_state = 'healthy'`,
		f.ws1, f.a).Scan(&reasonCode); err != nil {
		t.Fatalf("no transition row recorded for unknown -> healthy: %v", err)
	}
	if reasonCode == "" {
		t.Fatal("unknown -> healthy recorded with an empty reason_code")
	}
}

// Deleting a mailbox that has warmup history must succeed. The observations table's
// send FK nulls warmup_send_id when a send is deleted, so a CHECK requiring that
// column for placements aborted the referential action: removing mailbox B cascaded
// to the A→B send, which tried to null the column on A's surviving placement row and
// failed the constraint. DELETE /mailboxes/{id} 500'd for every warmup participant.
// Also asserts A's evidence OUTLIVES the send — deleting B must not erase what the
// pool learned about A.
func TestDeleteMailboxWithWarmupHistorySucceedsAndKeepsSenderEvidence(t *testing.T) {
	ctx, f := setupWarmup(t)

	sid := warmupSendUUID(t, ctx, f)
	if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
		WorkspaceID: f.ws1, WarmupSendID: sid, RecipientMailbox: f.b,
		ReceiptID: uuid.New(), Placement: placementInbox,
		ObservedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("seed placement: %v", err)
	}

	// B is the RECIPIENT. The surviving observation is attributed to A, so it is not
	// cascaded away with B — it is exactly the row whose send anchor gets nulled.
	n, err := f.q.DeleteMailbox(ctx, gen.DeleteMailboxParams{ID: f.b, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("DeleteMailbox with warmup history: %v", err)
	}
	if n != 1 {
		t.Fatalf("DeleteMailbox affected %d rows, want 1", n)
	}

	var count int
	var sendIDNull bool
	if err := f.raw.QueryRow(ctx,
		`SELECT count(*), bool_and(warmup_send_id IS NULL) FROM warmup_observations
		 WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'`,
		f.ws1, f.a).Scan(&count, &sendIDNull); err != nil {
		t.Fatalf("read A observations: %v", err)
	}
	if count != 1 {
		t.Fatalf("A placement observations = %d, want 1 (deleting the recipient must not erase the sender's evidence)", count)
	}
	if !sendIDNull {
		t.Fatal("expected the deleted send's anchor to be nulled, not retained")
	}
}

// warmupSendUUID drives one A->B send and returns its id parsed, which is the form
// the observation writers take.
func warmupSendUUID(t *testing.T, ctx context.Context, f warmupFixture) uuid.UUID {
	t.Helper()
	sendID, _ := makeWarmupSend(t, ctx, f)
	u, err := uuid.Parse(sendID)
	if err != nil {
		t.Fatalf("parse send id: %v", err)
	}
	return u
}

// Original-Message-ID is parsed out of an inbound DSN body and is therefore fully
// attacker-controlled. Without an observer binding, a forged DSN delivered to ANY
// connected mailbox in the workspace — a public alias, a support inbox — writes a
// TRUSTED hard bounce attributed to a DIFFERENT mailbox. Since Phase 1 that can
// quarantine the sender and fail its campaign's preflight, so the binding is a
// security control, not a filter.
func TestRecordWarmupHardBounceRequiresTheObservingMailboxToBeTheSender(t *testing.T) {
	ctx, f := setupWarmup(t)

	sendID, _ := makeWarmupSend(t, ctx, f) // A -> B
	messageID := "<" + sendID + "@acme.test>"

	// B is a real, same-workspace mailbox — but it is the RECIPIENT, not the sender.
	// A DSN for A's send arriving at B is either a misdelivery or a forgery.
	matched, err := f.core.(coreapi.WarmupEvidenceClient).
		RecordWarmupHardBounce(ctx, f.ws1.String(), messageID, f.b.String())
	if err != nil {
		t.Fatalf("RecordWarmupHardBounce: %v", err)
	}
	if matched {
		t.Fatal("a DSN observed by a mailbox that did not send the message must not match")
	}
	assertWarmupHardBounces(t, ctx, f, f.a, 0)

	// The same DSN arriving at the actual sender is the legitimate case.
	matched, err = f.core.(coreapi.WarmupEvidenceClient).
		RecordWarmupHardBounce(ctx, f.ws1.String(), messageID, f.a.String())
	if err != nil {
		t.Fatalf("RecordWarmupHardBounce (sender): %v", err)
	}
	if !matched {
		t.Fatal("a DSN observed by the sending mailbox must match")
	}
	assertWarmupHardBounces(t, ctx, f, f.a, 1)
}

func assertWarmupHardBounces(t *testing.T, ctx context.Context, f warmupFixture, mailbox uuid.UUID, want int) {
	t.Helper()
	var got int
	if err := f.raw.QueryRow(ctx,
		`SELECT count(*) FROM warmup_observations
		 WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'hard_bounce'`,
		f.ws1, mailbox).Scan(&got); err != nil {
		t.Fatalf("count hard bounces: %v", err)
	}
	if got != want {
		t.Fatalf("hard-bounce observations for %s = %d, want %d", mailbox, got, want)
	}
}

// Disabling deletes the participant row, so a fresh enable would fall back to
// lane='probation' — and DELETE-then-PUT is two calls any member with
// mailboxes:write can make. That would release a quarantined or blocked mailbox
// into a lane that may send and may take new campaign leads. The transition trail
// survives the delete and is what restores containment.
func TestReEnablingWarmupDoesNotClearContainment(t *testing.T) {
	ctx, f := setupWarmup(t)

	for _, sealed := range []string{warmup.LaneQuarantine, warmup.LaneBlocked} {
		t.Run(sealed, func(t *testing.T) {
			if _, err := f.raw.Exec(ctx,
				`INSERT INTO warmup_state_transitions (
				    workspace_id, mailbox_id, from_state, to_state, from_lane, to_lane,
				    reason_code, reason, lane_reason_code, lane_reason,
				    placement_samples, spam_rate, bounce_samples, bounce_rate,
				    complaint_samples, complaint_rate, invalid_tokens, policy_version)
				 VALUES ($1,$2,'healthy','healthy','healthy',$3,
				         'health_unchanged','x','lane_quarantined','x',
				         0,0,0,0,0,0,0,'test')`,
				f.ws1, f.a, sealed); err != nil {
				t.Fatalf("seed transition: %v", err)
			}

			if _, err := f.q.DisableWarmupParticipant(ctx, gen.DisableWarmupParticipantParams{
				MailboxID: f.a, WorkspaceID: f.ws1,
			}); err != nil {
				t.Fatalf("disable: %v", err)
			}
			p, err := f.q.UpsertWarmupParticipant(ctx, gen.UpsertWarmupParticipantParams{
				MailboxID: f.a, WorkspaceID: f.ws1,
				StartVolume: 8, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
			})
			if err != nil {
				t.Fatalf("re-enable: %v", err)
			}
			if p.Lane != sealed {
				t.Fatalf("lane after re-enable = %q, want %q: disable+enable must not release containment", p.Lane, sealed)
			}
		})
	}
}

// The cooldown is derived from the newest transition that MOVED a participant into
// quarantine. Health-only rows written WHILE quarantined carry
// from_lane = to_lane = 'quarantine'; counting those restarted the clock every time
// the mailbox made recovery progress, so improving extended the containment that
// improvement was meant to end.
func TestQuarantineCooldownIsNotRestartedByHealthOnlyTransitions(t *testing.T) {
	ctx, f := setupWarmup(t)

	insert := func(fromLane, toLane string, ageHours int) {
		t.Helper()
		if _, err := f.raw.Exec(ctx,
			`INSERT INTO warmup_state_transitions (
			    workspace_id, mailbox_id, from_state, to_state, from_lane, to_lane,
			    reason_code, reason, lane_reason_code, lane_reason,
			    placement_samples, spam_rate, bounce_samples, bounce_rate,
			    complaint_samples, complaint_rate, invalid_tokens, policy_version, created_at)
			 VALUES ($1,$2,'paused','paused',$3,$4,'x','x','y','y',
			         0,0,0,0,0,0,0,'test', now() - make_interval(hours => $5))`,
			f.ws1, f.a, fromLane, toLane, ageHours); err != nil {
			t.Fatalf("seed transition: %v", err)
		}
	}
	insert(warmup.LaneHealthy, warmup.LaneQuarantine, 96) // entry, 96h ago
	insert(warmup.LaneQuarantine, warmup.LaneQuarantine, 1)
	insert(warmup.LaneQuarantine, warmup.LaneQuarantine, 0) // held rows since

	rows, err := f.q.ListWarmupEvaluationRows(ctx, gen.ListWarmupEvaluationRowsParams{
		WorkspaceID: f.ws1, EvidenceTtlSeconds: 600,
	})
	if err != nil {
		t.Fatalf("ListWarmupEvaluationRows: %v", err)
	}
	var since time.Time
	for _, r := range rows {
		if r.MailboxID == f.a {
			since = r.QuarantinedSince.Time
		}
	}
	if since.IsZero() {
		t.Fatal("no quarantine entry time derived")
	}
	if elapsed := time.Since(since); elapsed < warmup.QuarantineCooldown {
		t.Fatalf("quarantined_since is %s ago, want >= the %s cooldown: held rows must not restart the clock",
			elapsed.Round(time.Hour), warmup.QuarantineCooldown)
	}
}

// seedWarmupBounceEvidence gives A a WARMUP bounce population that clears the
// minimum-sample gate: `delivered` sent warmup messages in the 30-day window and
// `bounces` trusted hard-bounce observations attributed to A as the sender. The
// sends reuse the thread and token of a real send, so nothing about the row shape
// is invented; only the volume is.
func seedWarmupBounceEvidence(t *testing.T, ctx context.Context, f warmupFixture, anchor uuid.UUID, delivered, bounces int) {
	t.Helper()
	if _, err := f.raw.Exec(ctx,
		`INSERT INTO warmup_sends (id, workspace_id, thread_id, from_mailbox, to_mailbox,
		                           status, token, sent_at)
		 SELECT gen_random_uuid(), s.workspace_id, s.thread_id, s.from_mailbox, s.to_mailbox,
		        'sent', s.token, now()
		   FROM warmup_sends s, generate_series(1, $2) g
		  WHERE s.id = $1`, anchor, delivered); err != nil {
		t.Fatalf("seed warmup sends: %v", err)
	}
	if _, err := f.raw.Exec(ctx,
		`INSERT INTO warmup_observations (workspace_id, mailbox_id, kind, source,
		                                  attribution_trusted, idempotency_key, observed_at)
		 SELECT $1, $2::uuid, 'hard_bounce', 'dsn', true, 'it-bounce:' || $2::text || ':' || g::text, now()
		   FROM generate_series(1, $3) g`, f.ws1, f.a, bounces); err != nil {
		t.Fatalf("seed hard-bounce observations: %v", err)
	}
}

// newestTransition reads the newest transition row for a mailbox.
func newestTransition(t *testing.T, ctx context.Context, f warmupFixture, mailbox uuid.UUID) gen.WarmupStateTransition {
	t.Helper()
	rows, err := f.q.ListWarmupTransitions(ctx, gen.ListWarmupTransitionsParams{
		WorkspaceID: f.ws1, MailboxID: mailbox, RowLimit: 1,
	})
	if err != nil {
		t.Fatalf("ListWarmupTransitions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("transitions for %s = %d, want 1", mailbox, len(rows))
	}
	r := rows[0]
	return gen.WarmupStateTransition{
		ID: r.ID, FromState: r.FromState, ToState: r.ToState, ReasonCode: r.ReasonCode,
		BouncePopulation: r.BouncePopulation, BounceSamples: r.BounceSamples, BounceRate: r.BounceRate,
	}
}

// The table has ONE bounce column pair and two populations that are deliberately
// never pooled, so a row that does not name its population is unreadable: this
// mailbox is paused by WARMUP bounces while its campaign denominator is zero, and
// an unlabelled "300 samples at 15.8%" beside reason_code='warmup_bounce_pause'
// invites the reader to attribute the figure to campaign mail.
func TestTransitionNamesTheBouncePopulationThatDroveIt(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")

	anchor := warmupSendUUID(t, ctx, f)
	// 15 hard bounces on 60 warmup sends: 25% observed, whose Wilson lower bound
	// (~15.8%) clears the 10% pause band. A has sent no campaign mail at all.
	seedWarmupBounceEvidence(t, ctx, f, anchor, 59, 15)

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}

	if health, _ := participantAxes(t, ctx, f, f.a); health != warmup.StatePaused {
		t.Fatalf("A health = %q, want paused — the fixture must actually trip the warmup bounce band", health)
	}
	got := newestTransition(t, ctx, f, f.a)
	if got.BouncePopulation == nil {
		t.Fatal("bounce_population is NULL on a row this engine wrote: the pair has no population to belong to")
	}
	if *got.BouncePopulation != warmup.BouncePopulationWarmup {
		t.Fatalf("bounce_population = %q, want %q — the warmup arm drove this decision",
			*got.BouncePopulation, warmup.BouncePopulationWarmup)
	}
	// The denominator must be the named population's own. The campaign arm is
	// empty here, so recording the campaign pair would report 0 samples beside a
	// pause the evidence plainly justifies.
	if got.BounceSamples != 60 {
		t.Fatalf("bounce_samples = %d, want 60 (the warmup denominator, not the empty campaign one)", got.BounceSamples)
	}
	if got.BounceRate <= 0.10 {
		t.Fatalf("bounce_rate = %v, want the warmup arm's rate above the 10%% pause band", got.BounceRate)
	}
}

// seedAuthPassing caches a 'passing' DNS verdict for a mailbox's organizational
// domain, which is the lane's admission prerequisite. Without it every lane
// decision collapses to pending_auth and says nothing about evidence.
func seedAuthPassing(t *testing.T, ctx context.Context, f warmupFixture, ws uuid.UUID, domain string) {
	t.Helper()
	if _, err := f.raw.Exec(ctx,
		`INSERT INTO sending_domains (workspace_id, domain, state, spf_found, dkim_found, dmarc_found, checked_at)
		 VALUES ($1,$2,'passing',true,false,true,now())
		 ON CONFLICT (workspace_id, domain) DO UPDATE SET state = 'passing'`, ws, domain); err != nil {
		t.Fatalf("seed sending domain: %v", err)
	}
}

// seedPlacementsAged records n trusted inbox placements attributed to A as the
// SENDER, all observed at the same age.
func seedPlacementsAged(t *testing.T, ctx context.Context, f warmupFixture, sid uuid.UUID, n int, age time.Duration) {
	t.Helper()
	observed := pgtype.Timestamptz{Time: time.Now().Add(-age), Valid: true}
	for i := 0; i < n; i++ {
		if err := f.q.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
			WorkspaceID: f.ws1, WarmupSendID: sid, RecipientMailbox: f.b,
			ReceiptID: uuid.New(), Placement: placementInbox, ObservedAt: observed,
		}); err != nil {
			t.Fatalf("seed placement %d: %v", i, err)
		}
	}
}

// withWallClock returns the fixture with the evaluator reading the real clock.
//
// setupWarmup pins it to noon on a future warmup-busy day so SEND scheduling is
// deterministic, but the evaluator compares that instant against timestamps
// Postgres stamped with its own now() — so any assertion about an elapsed grace
// or cooldown would be measuring the gap between the two clocks instead. The
// health sweep does not schedule anything, so the wall clock is safe here.
func withWallClock(t *testing.T, f warmupFixture) warmupFixture {
	t.Helper()
	impl, ok := f.core.(client)
	if !ok {
		t.Fatalf("core is %T, want inprocess.client — cannot restore the wall clock", f.core)
	}
	impl.now = time.Now
	f.core = impl
	return f
}

// seedWarmupSendRow inserts a SENT warmup send directly, bypassing the scheduler.
//
// The scheduler is the wrong dependency for a test that only needs a send row to
// hang observations off. warmupSendFrom goes through GetWarmupSendJob, which
// consults warmup.EffectiveDailyVolume — and that hashes the mailbox id into a
// per-day skip decision. setupWarmup pins the clock to a day generous to both
// fixture mailboxes, but the guard tests call withWallClock to compare evaluator
// timestamps against Postgres now(), which throws that pinning away. So they rolled
// the volume dice against today's real date with a fresh random uuid, and lost
// about one run in eight, reporting only "job={Skip:true}".
//
// Seeding the row makes those tests deterministic AND narrows what they assert: a
// guard about whether recording X changes health or lane should not also be a test
// of the send scheduler.
func seedWarmupSendRow(t *testing.T, ctx context.Context, f warmupFixture, from, to uuid.UUID) uuid.UUID {
	t.Helper()
	thread, send := uuid.New(), uuid.New()
	if _, err := f.raw.Exec(ctx,
		`INSERT INTO warmup_threads (id, workspace_id, sender_mailbox, partner_mailbox, subject, content_key)
		 VALUES ($1, $2, $3, $4, 'seeded thread', 'seed')`,
		thread, f.ws1, from, to); err != nil {
		t.Fatalf("seed warmup thread: %v", err)
	}
	if _, err := f.raw.Exec(ctx,
		`INSERT INTO warmup_sends (id, workspace_id, thread_id, from_mailbox, to_mailbox, status, token, message_id, sent_at)
		 VALUES ($1, $2, $3, $4, $5, 'sent', 'seeded-token', $6, now())`,
		send, f.ws1, thread, from, to, "<"+send.String()+"@acme.test>"); err != nil {
		t.Fatalf("seed warmup send: %v", err)
	}
	return send
}

func participantAxes(t *testing.T, ctx context.Context, f warmupFixture, mailbox uuid.UUID) (health, lane string) {
	t.Helper()
	p, err := f.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: mailbox, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("read participant: %v", err)
	}
	return p.HealthState, p.Lane
}

// Freshness must measure the age of the EVIDENCE, not the age of the snapshot row.
// The snapshot is rewritten by the statement immediately before the read, so a
// computed_at-based test was true on every tick — a staleness rule that could not
// detect staleness. Here the placements are plentiful (30, well over
// MinPlacementSamples) and inside the 7-day sample window, but every one of them
// is older than the freshness TTL: the mailbox has not been seen for days and must
// read as unmeasured, never as healthy.
func TestFreshnessMeasuresEvidenceAgeNotSnapshotAge(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")
	sid := warmupSendUUID(t, ctx, f)
	seedPlacementsAged(t, ctx, f, sid, 30, warmupEvidenceTTL+2*time.Hour)

	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth: %v", err)
	}
	health, lane := participantAxes(t, ctx, f, f.a)
	if health != warmup.StateUnknown {
		t.Fatalf("A health = %q, want unknown: 30 placements that are all older than the TTL are not fresh evidence", health)
	}
	if lane == warmup.LaneHealthy {
		t.Fatal("A was promoted to the healthy lane on evidence nobody has refreshed for days")
	}

	// Control: the same 30 observations, observed NOW, do qualify. Without this the
	// assertion above would also pass if the query were simply broken.
	seedPlacementsAged(t, ctx, f, sid, 30, 0)
	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("EvaluateWarmupHealth (fresh): %v", err)
	}
	if health, lane = participantAxes(t, ctx, f, f.a); health != warmup.StateHealthy || lane != warmup.LaneHealthy {
		t.Fatalf("A = %s/%s, want healthy/healthy once the same evidence is fresh", health, lane)
	}
}

// End-to-end hysteresis: a lapse costs the healthy lane only once it has PERSISTED
// past LaneEvidenceGrace. The grace is anchored on the transition trail, so this
// exercises the lateral that derives it as well as the policy that reads it.
func TestEvidenceLapseCostsTheHealthyLaneOnlyAfterTheGrace(t *testing.T) {
	ctx, f := setupWarmup(t)
	f = withWallClock(t, f)
	seedAuthPassing(t, ctx, f, f.ws1, "acme.test")
	sid := warmupSendUUID(t, ctx, f)

	// Sweep 1: fresh qualified evidence puts A in the healthy lane.
	seedPlacementsAged(t, ctx, f, sid, 25, 0)
	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("sweep 1: %v", err)
	}
	if health, lane := participantAxes(t, ctx, f, f.a); health != warmup.StateHealthy || lane != warmup.LaneHealthy {
		t.Fatalf("after sweep 1 A = %s/%s, want healthy/healthy", health, lane)
	}

	// The evidence ages out from under it — the pool stalled, a partner stopped
	// polling. Sweep 2 sees no fresh evidence.
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_observations SET observed_at = now() - make_interval(secs => $3)
		  WHERE workspace_id = $1 AND mailbox_id = $2`,
		f.ws1, f.a, int(warmupEvidenceTTL.Seconds())+3600); err != nil {
		t.Fatalf("age the evidence: %v", err)
	}
	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("sweep 2: %v", err)
	}
	health, lane := participantAxes(t, ctx, f, f.a)
	if health != warmup.StateUnknown {
		t.Fatalf("after sweep 2 A health = %q, want unknown", health)
	}
	if lane != warmup.LaneHealthy {
		t.Fatalf("after sweep 2 A lane = %q, want healthy: one unqualified tick must not cost the lane", lane)
	}

	// Sweep 3, five minutes later in the real system: still inside the grace.
	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("sweep 3: %v", err)
	}
	if _, lane = participantAxes(t, ctx, f, f.a); lane != warmup.LaneHealthy {
		t.Fatalf("after sweep 3 A lane = %q, want healthy: the grace has not elapsed", lane)
	}

	// Age the recorded lapse past the grace. The next sweep demotes: the grace is a
	// delay, not an exemption.
	//
	if _, err := f.raw.Exec(ctx,
		`UPDATE warmup_state_transitions SET created_at = created_at - make_interval(secs => $3)
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND to_state = 'unknown'`,
		f.ws1, f.a, int(warmup.LaneEvidenceGrace.Seconds())+60); err != nil {
		t.Fatalf("age the lapse: %v", err)
	}
	if err := f.core.EvaluateWarmupHealth(ctx); err != nil {
		t.Fatalf("sweep 4: %v", err)
	}
	if _, lane = participantAxes(t, ctx, f, f.a); lane != warmup.LaneProbation {
		t.Fatalf("after sweep 4 A lane = %q, want probation: a lapse that outlasts the grace demotes", lane)
	}
}

// placementObservations returns how many trusted placement observations of each
// kind are attributed to a mailbox as the SENDER — the rows the snapshot counts.
func placementObservations(t *testing.T, ctx context.Context, f warmupFixture, sender uuid.UUID) (inbox, spam, total int) {
	t.Helper()
	if err := f.raw.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE placement = 'inbox'),
		        count(*) FILTER (WHERE placement = 'spam'),
		        count(*)
		   FROM warmup_observations
		  WHERE workspace_id = $1 AND mailbox_id = $2 AND kind = 'placement'`,
		f.ws1, sender).Scan(&inbox, &spam, &total); err != nil {
		t.Fatalf("read placement observations: %v", err)
	}
	return inbox, spam, total
}

// A message seen in the INBOX and later found in junk used to keep
// placement='inbox' forever: the receipt upsert short-circuits on the duplicate,
// before the observation write. Spam-after-inbox is the most important placement
// change there is, so the later observation supersedes the earlier one — as ONE
// row, so the correction moves the count instead of adding to it.
func TestSpamAfterInboxSupersedesThePlacement(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f) // A -> B, sender A

	in := coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID,
		RecipientMailbox: recipient, Placement: placementInbox,
	}
	if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
		t.Fatalf("inbox receipt: %v", err)
	}
	if inbox, spam, total := placementObservations(t, ctx, f, f.a); inbox != 1 || spam != 0 || total != 1 {
		t.Fatalf("after the inbox poll: inbox=%d spam=%d total=%d, want 1/0/1", inbox, spam, total)
	}

	// The next poll finds the same message in junk.
	in.Placement = placementSpam
	plan, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("spam re-poll: %v", err)
	}

	inbox, spam, total := placementObservations(t, ctx, f, f.a)
	if spam != 1 {
		t.Fatalf("spam observations = %d, want 1: the reclassification was discarded", spam)
	}
	if inbox != 0 {
		t.Fatalf("inbox observations = %d, want 0: the superseded placement must not still count", inbox)
	}
	if total != 1 {
		t.Fatalf("placement observations = %d, want 1: one message must not become two samples", total)
	}

	// The daily projection the overview and the deliverability score read moves with
	// it, so the two views of one message cannot disagree.
	if _, aInbox, aSpam, _ := todayStats(t, ctx, f, f.a); aInbox != 0 || aSpam != 1 {
		t.Fatalf("sender A daily stats inbox=%d spam=%d, want 0/1", aInbox, aSpam)
	}

	// And the still-unengaged receipt's rebuilt plan now rescues the message out of
	// junk, which the stored inbox placement would never have asked for.
	if !plan.DoRescue {
		t.Fatal("the rebuilt plan does not rescue a message now known to be in spam")
	}
}

// The correction is monotone and exactly-once. It must not flap back — the
// engager RESCUES spam into the inbox, so a later poll legitimately finds it
// there, and honouring that would let our own rescue erase the evidence that the
// rescue was needed. Nor may a repeated spam poll count twice.
func TestPlacementReclassificationIsMonotoneAndCountsOnce(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	in := coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID,
		RecipientMailbox: recipient, Placement: placementInbox,
	}
	if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
		t.Fatalf("inbox receipt: %v", err)
	}
	in.Placement = placementSpam
	for i := 0; i < 3; i++ {
		if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
			t.Fatalf("spam re-poll %d: %v", i, err)
		}
	}
	if inbox, spam, total := placementObservations(t, ctx, f, f.a); inbox != 0 || spam != 1 || total != 1 {
		t.Fatalf("after three spam polls: inbox=%d spam=%d total=%d, want 0/1/1", inbox, spam, total)
	}
	if _, aInbox, aSpam, _ := todayStats(t, ctx, f, f.a); aInbox != 0 || aSpam != 1 {
		t.Fatalf("repeated polls moved the counters twice: inbox=%d spam=%d, want 0/1", aInbox, aSpam)
	}

	// Now the rescue lands and the message is seen in the inbox again.
	in.Placement = placementInbox
	if _, err := f.core.RecordWarmupReceipt(ctx, in); err != nil {
		t.Fatalf("post-rescue inbox poll: %v", err)
	}
	if inbox, spam, _ := placementObservations(t, ctx, f, f.a); inbox != 0 || spam != 1 {
		t.Fatalf("evidence flapped back after a rescue: inbox=%d spam=%d, want 0/1", inbox, spam)
	}
	if _, aInbox, aSpam, _ := todayStats(t, ctx, f, f.a); aInbox != 0 || aSpam != 1 {
		t.Fatalf("daily stats flapped back after a rescue: inbox=%d spam=%d, want 0/1", aInbox, aSpam)
	}
}

// An ALREADY-ENGAGED receipt still records the reclassification. Whether we opened
// the message has nothing to do with where it landed, and the placement axis is the
// signal the whole policy leans on.
func TestSpamAfterInboxIsRecordedEvenOnceEngaged(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)

	in := coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID,
		RecipientMailbox: recipient, Placement: placementInbox,
	}
	plan, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("inbox receipt: %v", err)
	}
	if err := f.core.MarkWarmupEngaged(ctx, plan.ReceiptID, f.ws1.String(), false); err != nil {
		t.Fatalf("MarkWarmupEngaged: %v", err)
	}

	in.Placement = placementSpam
	engagedPlan, err := f.core.RecordWarmupReceipt(ctx, in)
	if err != nil {
		t.Fatalf("spam re-poll after engagement: %v", err)
	}
	if engagedPlan != (coreapi.WarmupEngagePlan{}) {
		t.Fatalf("an engaged receipt must not be re-engaged: %+v", engagedPlan)
	}
	if inbox, spam, total := placementObservations(t, ctx, f, f.a); inbox != 0 || spam != 1 || total != 1 {
		t.Fatalf("engaged reclassification: inbox=%d spam=%d total=%d, want 0/1/1", inbox, spam, total)
	}
}
