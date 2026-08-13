//go:build integration

package inprocess

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
	"github.com/inroad/inroad/internal/platform/sendcap"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These tests exercise health-gated cold sending and the campaign-wide daily limit
// against Postgres, on the sender-pool fixture (setupPool). Docker must be up.

// setHealth makes the mailbox a warmup participant in the given health state.
func (f poolFixture) setHealth(t *testing.T, ctx context.Context, mailboxID uuid.UUID, state string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO warmup_participants (mailbox_id, workspace_id, health_state)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (mailbox_id) DO UPDATE SET health_state = EXCLUDED.health_state`,
		mailboxID, f.ws, state); err != nil {
		t.Fatalf("warmup participant %s: %v", state, err)
	}
}

// disableWarmup turns warmup off for a mailbox while leaving its health_state
// behind, which is how a stale 'paused' comes to exist on a mailbox no health sweep
// will ever re-evaluate.
func (f poolFixture) disableWarmup(t *testing.T, ctx context.Context, mailboxID uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE warmup_participants SET enabled = false WHERE mailbox_id = $1`, mailboxID); err != nil {
		t.Fatalf("disable warmup: %v", err)
	}
}

// setDailyLimit sets (or with nil clears) the campaign-wide daily limit.
func (f poolFixture) setDailyLimit(t *testing.T, ctx context.Context, limit *int32) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE campaigns SET daily_limit = $2 WHERE id = $1`, f.campaignID, limit); err != nil {
		t.Fatalf("set daily_limit: %v", err)
	}
}

// pinThread pins the enrollment to a mailbox with step 1 already sent, i.e. a thread
// in flight whose mailbox can no longer change.
func (f poolFixture) pinThread(t *testing.T, ctx context.Context, enrollmentID, mailboxID uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE sequence_enrollments SET mailbox_id = $2, current_step = 1 WHERE id = $1`,
		enrollmentID, mailboxID); err != nil {
		t.Fatalf("pin thread: %v", err)
	}
}

func (f poolFixture) enrollmentStatus(t *testing.T, ctx context.Context, enrollmentID uuid.UUID) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(ctx,
		`SELECT status FROM sequence_enrollments WHERE id = $1`, enrollmentID).Scan(&status); err != nil {
		t.Fatalf("read enrollment status: %v", err)
	}
	return status
}

func (f poolFixture) sendCount(t *testing.T, ctx context.Context) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM sends WHERE campaign_id = $1`, f.campaignID).Scan(&n); err != nil {
		t.Fatalf("count sends: %v", err)
	}
	return n
}

// The point of the whole feature: the warmup engine has paused a mailbox, so cold
// volume goes to its healthy peer instead — even though the paused one is heavily
// weighted and has its full capacity free.
func TestPausedPoolMemberIsSkippedForItsHealthyPeer(t *testing.T) {
	ctx, f := setupPool(t)
	f.setRotationMode(t, ctx, rotation.ModeWeighted)
	f.addSender(t, ctx, f.mailboxA, 100, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	f.setHealth(t, ctx, f.mailboxA, sendcap.HealthPaused)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxB.String() {
		t.Errorf("mailbox = %s, want the healthy peer B %s", job.MailboxID, f.mailboxB)
	}
	if job.HealthPaused {
		t.Error("HealthPaused = true although a healthy peer took the contact")
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != f.mailboxB {
		t.Errorf("pinned mailbox = %s, want B %s", got, f.mailboxB)
	}
}

// Invariant 3, end to end: a pool whose ONLY member is paused must DEFER. The
// enrollment stays active with nothing sent, because the mailbox may recover and the
// thread cannot be moved to another one.
func TestPausedOnlyPoolDefersAndLeavesTheEnrollmentActive(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.setHealth(t, ctx, f.mailboxA, sendcap.HealthPaused)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob must defer, not error: %v", err)
	}
	if job.Skip {
		t.Fatal("a gated pool must defer, not skip the enrollment")
	}
	// Either signal routes to the deferral, but it must NEVER look like the
	// degenerate cap (cap <= 0) that stops an enrollment.
	overCap := job.EffectiveDailyCap > 0 && job.SentToday >= job.EffectiveDailyCap
	if !job.HealthPaused && !overCap {
		t.Errorf("job = {HealthPaused:%v cap:%d sent:%d}, want an explicit deferral",
			job.HealthPaused, job.EffectiveDailyCap, job.SentToday)
	}
	if !job.HealthPaused {
		t.Error("HealthPaused = false for a pool whose only member is paused")
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Errorf("a deferred send pinned mailbox %s; nothing was chosen", got)
	}
	if status := f.enrollmentStatus(t, ctx, enrollmentID); status != "active" {
		t.Errorf("enrollment status = %q, want active — a gated pool defers, it does not stop", status)
	}
	if n := f.sendCount(t, ctx); n != 0 {
		t.Errorf("sends = %d, want none from a fully paused pool", n)
	}
}

// A throttled mailbox stops at HALF its ramped cap. Gating that only lowered the
// selector's score would let it keep sending right up to the full 100.
func TestThrottledMailboxStopsAtHalfItsRampedCap(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.setHealth(t, ctx, f.mailboxA, sendcap.HealthThrottled)

	// 49 of the throttled cap of 50 used: still sending.
	f.fillToCap(t, ctx, f.mailboxA, 49)
	job, err := f.core.GetStepSendJob(ctx, f.enroll(t, ctx).String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Fatalf("mailbox = %s, want A %s", job.MailboxID, f.mailboxA)
	}
	if job.EffectiveDailyCap != 50 {
		t.Errorf("effective cap = %d, want 50 (a ramped 100 halved by 'throttled')", job.EffectiveDailyCap)
	}
	if job.SentToday >= job.EffectiveDailyCap {
		t.Errorf("cap/sent = %d/%d, want it still under the throttled cap", job.EffectiveDailyCap, job.SentToday)
	}

	// The 50th send fills it, and the pool defers with the mailbox's own cap only
	// half consumed in absolute terms.
	f.fillToCap(t, ctx, f.mailboxA, 1)
	deferred, err := f.core.GetStepSendJob(ctx, f.enroll(t, ctx).String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if deferred.EffectiveDailyCap <= 0 || deferred.SentToday < deferred.EffectiveDailyCap {
		t.Errorf("cap/sent = %d/%d, want a cap-deferral at the throttled ceiling",
			deferred.EffectiveDailyCap, deferred.SentToday)
	}
}

// Invariant 2 — the easy half to miss. A thread ALREADY pinned to a mailbox that
// later degrades must be throttled on its next step. Gating only at assignment would
// leave every already-enrolled contact sending at full volume from a mailbox in
// trouble, which is most of the volume.
func TestPinnedThreadIsThrottledWhenItsMailboxDegrades(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	enrollmentID := f.enroll(t, ctx)
	f.pinThread(t, ctx, enrollmentID, f.mailboxA)

	before, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if before.EffectiveDailyCap != 100 {
		t.Fatalf("effective cap = %d, want the ungated 100 before any health signal", before.EffectiveDailyCap)
	}

	f.setHealth(t, ctx, f.mailboxA, sendcap.HealthThrottled)
	after, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if after.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want the thread's mailbox A %s (gating must not re-route)", after.MailboxID, f.mailboxA)
	}
	if after.EffectiveDailyCap != 50 {
		t.Errorf("effective cap = %d, want 50 — the PINNED path must apply health too", after.EffectiveDailyCap)
	}

	// And at the throttled ceiling it defers rather than sending on.
	f.fillToCap(t, ctx, f.mailboxA, 50)
	capped, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if capped.SentToday < capped.EffectiveDailyCap {
		t.Errorf("cap/sent = %d/%d, want a deferral at the throttled ceiling",
			capped.EffectiveDailyCap, capped.SentToday)
	}
}

// A pinned thread whose mailbox is PAUSED defers, keeps its mailbox, and reports the
// mailbox's real cap — the pause travels as its own flag rather than as a faked
// zero cap, which would stop the enrollment.
func TestPinnedThreadDefersWhenItsMailboxIsPaused(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	enrollmentID := f.enroll(t, ctx)
	f.pinThread(t, ctx, enrollmentID, f.mailboxA)
	f.setHealth(t, ctx, f.mailboxA, sendcap.HealthPaused)

	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if !job.HealthPaused {
		t.Error("HealthPaused = false for a thread pinned to a paused mailbox")
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want A %s: a paused mailbox must not re-route the thread to B", job.MailboxID, f.mailboxA)
	}
	if job.EffectiveDailyCap != 100 {
		t.Errorf("effective cap = %d, want the mailbox's real 100 (reported numbers must be true)", job.EffectiveDailyCap)
	}
	if status := f.enrollmentStatus(t, ctx, enrollmentID); status != "active" {
		t.Errorf("enrollment status = %q, want active", status)
	}
}

// Disabling warmup freezes health_state, and the health sweep never revisits a
// disabled participant — so a stale 'paused' must not veto cold sending forever.
// Nothing could ever clear it.
func TestDisabledWarmupParticipantDoesNotGateColdSending(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.setHealth(t, ctx, f.mailboxA, sendcap.HealthPaused)
	f.disableWarmup(t, ctx, f.mailboxA)

	job, err := f.core.GetStepSendJob(ctx, f.enroll(t, ctx).String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.HealthPaused {
		t.Error("a DISABLED warmup participant's frozen 'paused' blocked cold sending")
	}
	if job.MailboxID != f.mailboxA.String() || job.EffectiveDailyCap != 100 {
		t.Errorf("job = {mailbox:%s cap:%d}, want A at its full cap", job.MailboxID, job.EffectiveDailyCap)
	}
}

// The campaign-wide limit: every mailbox has plenty of capacity, and the campaign
// still stops for the day. The two gates stack.
func TestCampaignDailyLimitDefersThoughEveryMailboxHasCapacity(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	limit := int32(5)
	f.setDailyLimit(t, ctx, &limit)
	// 5 sends today, spread over both mailboxes: neither is anywhere near its own
	// cap of 100, but the CAMPAIGN has used its allowance.
	f.fillToCap(t, ctx, f.mailboxA, 3)
	f.fillToCap(t, ctx, f.mailboxB, 2)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob must defer, not error: %v", err)
	}
	if !job.CampaignLimited {
		t.Fatalf("job = %+v, want CampaignLimited", job)
	}
	if job.Skip {
		t.Error("a campaign at its daily limit must defer, not skip")
	}
	// Invariant 5: a campaign limit must not masquerade as a mailbox that used up
	// its cap. No mailbox was resolved, so no mailbox figures are reported at all.
	if job.SentToday != 0 || job.EffectiveDailyCap != 0 {
		t.Errorf("cap/sent = %d/%d, want no mailbox figures on a campaign-limited job",
			job.EffectiveDailyCap, job.SentToday)
	}
	// No credential was opened and no mailbox pinned for a send that will not happen.
	if job.SMTPPassword != nil || job.AccessToken != nil {
		t.Error("a campaign-limited job opened a credential")
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Errorf("a campaign-limited deferral pinned mailbox %s", got)
	}
	if status := f.enrollmentStatus(t, ctx, enrollmentID); status != "active" {
		t.Errorf("enrollment status = %q, want active", status)
	}

	// One under the limit and it sends again — the gate is a ceiling, not a latch.
	raised := int32(9)
	f.setDailyLimit(t, ctx, &raised)
	resumed, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if resumed.CampaignLimited {
		t.Error("still CampaignLimited below the raised limit")
	}
	if resumed.MailboxID == "" {
		t.Error("no mailbox resolved once the campaign was back under its limit")
	}
}

// A NULL daily_limit is exactly today's behaviour: the campaign sends whatever its
// mailboxes allow, with no campaign-wide ceiling and no count taken.
func TestNullDailyLimitBehavesAsBefore(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.setDailyLimit(t, ctx, nil)
	f.fillToCap(t, ctx, f.mailboxA, 50) // well past any plausible campaign limit

	job, err := f.core.GetStepSendJob(ctx, f.enroll(t, ctx).String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.CampaignLimited {
		t.Error("CampaignLimited with no limit configured")
	}
	if job.MailboxID != f.mailboxA.String() {
		t.Errorf("mailbox = %s, want A %s", job.MailboxID, f.mailboxA)
	}
}

// Invariant 4, mailbox-first safety: a campaign limit far above the pool's capacity
// cannot raise a mailbox above its own cap. The limit only ever lowers throughput.
func TestGenerousCampaignLimitCannotRaiseMailboxThroughput(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	limit := int32(100000)
	f.setDailyLimit(t, ctx, &limit)
	f.fillToCap(t, ctx, f.mailboxA, 100) // the mailbox's own daily_cap

	job, err := f.core.GetStepSendJob(ctx, f.enroll(t, ctx).String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.CampaignLimited {
		t.Fatal("CampaignLimited far below the configured limit")
	}
	if job.EffectiveDailyCap <= 0 || job.SentToday < job.EffectiveDailyCap {
		t.Errorf("cap/sent = %d/%d, want the mailbox's own cap still enforced",
			job.EffectiveDailyCap, job.SentToday)
	}
}

// The campaign count is workspace-pinned and campaign-scoped: another campaign's
// volume, and another tenant's, must not consume this campaign's allowance.
func TestCampaignDailyLimitCountsOnlyItsOwnSends(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	limit := int32(2)
	f.setDailyLimit(t, ctx, &limit)

	// A second campaign in the same workspace, sharing the same mailbox, sends 5.
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
			`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, sent_at)
			 VALUES ($1,$2,$3,$4,'other@x.test','sent', now())`,
			f.ws, other.ID, c.ID, f.mailboxA); err != nil {
			t.Fatalf("other send: %v", err)
		}
	}

	job, err := f.core.GetStepSendJob(ctx, f.enroll(t, ctx).String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.CampaignLimited {
		t.Error("another campaign's sends consumed this campaign's daily limit")
	}
}

// setLane makes the mailbox an enabled warmup participant in the given pool lane.
// The lane is the pool-eligibility axis; health is left at its default so these
// tests cannot pass for the wrong reason.
func (f poolFixture) setLane(t *testing.T, ctx context.Context, mailboxID uuid.UUID, lane string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO warmup_participants (mailbox_id, workspace_id, lane)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (mailbox_id) DO UPDATE SET lane = EXCLUDED.lane, enabled = true`,
		mailboxID, f.ws, lane); err != nil {
		t.Fatalf("warmup participant lane %s: %v", lane, err)
	}
}

// Design §7: the campaign gate is mailbox AND organizational domain. A and B share
// acme.test, so quarantining A withholds B too — reputation is largely assessed per
// domain, and expanding cold volume from the clean sibling spends a standing that
// is already in trouble. The whole pool is therefore withheld and the send DEFERS
// with the enrollment intact, exactly as a fully paused pool does.
func TestQuarantinedSiblingWithholdsItsWholeDomain(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	f.setLane(t, ctx, f.mailboxA, warmup.LaneQuarantine)
	f.setLane(t, ctx, f.mailboxB, warmup.LaneHealthy)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob must defer, not error: %v", err)
	}
	if job.Skip {
		t.Fatal("a withheld pool must defer, not skip the enrollment")
	}
	if !job.HealthPaused {
		t.Error("HealthPaused = false: a pool withheld by its domain must defer explicitly")
	}
	if got := f.storedMailbox(t, ctx, enrollmentID); got != uuid.Nil {
		t.Errorf("a withheld pool pinned mailbox %s; nothing may be chosen", got)
	}
	if status := f.enrollmentStatus(t, ctx, enrollmentID); status != "active" {
		t.Errorf("enrollment status = %q, want active — containment defers, it does not stop", status)
	}
	if n := f.sendCount(t, ctx); n != 0 {
		t.Errorf("sends = %d, want none from a contained domain", n)
	}
}

// The domain gate must not overreach: a sibling merely gathering evidence
// (probation) restricts nothing, so the healthy mailbox still takes the contact.
// Without this the previous test would also pass if the gate simply refused every
// pool with any warmup participant in it.
func TestEvidenceGatheringSiblingDoesNotWithholdTheDomain(t *testing.T) {
	ctx, f := setupPool(t)
	f.addSender(t, ctx, f.mailboxA, 1, true)
	f.addSender(t, ctx, f.mailboxB, 1, true)
	f.setLane(t, ctx, f.mailboxA, warmup.LaneProbation)
	f.setLane(t, ctx, f.mailboxB, warmup.LaneHealthy)

	enrollmentID := f.enroll(t, ctx)
	job, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}
	if job.Skip || job.HealthPaused {
		t.Fatalf("job deferred (skip=%v paused=%v); a probation sibling withholds nothing",
			job.Skip, job.HealthPaused)
	}
	if job.MailboxID == "" {
		t.Fatal("no mailbox was chosen")
	}
}
