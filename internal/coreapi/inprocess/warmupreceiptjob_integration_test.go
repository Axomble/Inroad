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
func TestEvaluateWarmupHealthTransitionsAndRecovers(t *testing.T) {
	ctx, f := setupWarmup(t)

	// A: seed a spammy trailing window attributed to A as the SENDER via a real A->B
	// send (6 spam / 4 inbox = 60% > 50% → paused). RecordWarmupSenderPlacementStat
	// resolves the sender from warmup_sends.from_mailbox = A.
	sendID, _ := makeWarmupSend(t, ctx, f)
	sid, err := uuid.Parse(sendID)
	if err != nil {
		t.Fatalf("parse sendID: %v", err)
	}
	for i := 0; i < 6; i++ {
		if err := f.q.RecordWarmupSenderPlacementStat(ctx, gen.RecordWarmupSenderPlacementStatParams{WorkspaceID: f.ws1, WarmupSendID: sid, Placement: placementSpam}); err != nil {
			t.Fatalf("seed A spam: %v", err)
		}
	}
	for i := 0; i < 4; i++ {
		if err := f.q.RecordWarmupSenderPlacementStat(ctx, gen.RecordWarmupSenderPlacementStatParams{WorkspaceID: f.ws1, WarmupSendID: sid, Placement: placementInbox}); err != nil {
			t.Fatalf("seed A inbox: %v", err)
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
	if pa.HealthState != warmup.StatePaused {
		t.Fatalf("A health = %q, want paused (60%% sender-attributed spam)", pa.HealthState)
	}
	if !pa.PausedUntil.Valid {
		t.Fatalf("A paused_until not set on escalation")
	}

	pb, err := f.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: f.b, WorkspaceID: f.ws1})
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if pb.HealthState != warmup.StateThrottled {
		t.Fatalf("B health = %q, want throttled (one-level recovery after block elapsed)", pb.HealthState)
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
