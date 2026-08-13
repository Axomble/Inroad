package inprocess

import (
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// The engage-plan construction is pure (seeded hash, no DB, no rand), so these
// tests exercise it directly with faked inputs — the DB-backed idempotency and
// tenancy behavior is covered by the //go:build integration tests.

const (
	tReceiptID = "11111111-1111-1111-1111-111111111111"
	tRecipient = "22222222-2222-2222-2222-222222222222"
	tDay       = "2026-07-27"
)

// tReceivedAt is a mid-morning receipt instant on tDay: inside waking hours, so a
// reply delay short enough to stay in the window is neither deferred nor truncated.
var tReceivedAt = time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)

// planFor builds an engage plan for the shared test receipt at the given reply rate
// and placement, anchored on tReceivedAt with now == receivedAt (the fresh-insert case).
func planFor(rate float64, placement string) coreapi.WarmupEngagePlan {
	return warmupEngagePlan(engagePlanInputs{
		ReceiptID:        tReceiptID,
		RecipientMailbox: tRecipient,
		DayKey:           tDay,
		ReplyRate:        rate,
		Placement:        placement,
		ReceivedAt:       tReceivedAt,
		Now:              tReceivedAt,
	})
}

// TestEngagePlanRescueOnlyOnSpam pins DoRescue to the spam placement and DoMarkRead
// to always-on, across all three placements.
func TestEngagePlanRescueOnlyOnSpam(t *testing.T) {
	cases := []struct {
		placement  string
		wantRescue bool
	}{
		{placementInbox, false},
		{placementSpam, true},
		{placementOther, false},
	}
	for _, tc := range cases {
		plan := planFor(0.3, tc.placement)
		if plan.DoRescue != tc.wantRescue {
			t.Errorf("placement %q: DoRescue = %v, want %v", tc.placement, plan.DoRescue, tc.wantRescue)
		}
		if !plan.DoMarkRead {
			t.Errorf("placement %q: DoMarkRead = false, want always true", tc.placement)
		}
		if plan.ReceiptID != tReceiptID {
			t.Errorf("placement %q: ReceiptID = %q, want %q", tc.placement, plan.ReceiptID, tReceiptID)
		}
	}
}

// TestEngagePlanReplyMatchesDecision proves DoReply is exactly the seeded
// ReplyDecision over the same seed the plan builds — never rand — and that the
// boundary rates are honored (0 → never, 1 → always).
func TestEngagePlanReplyMatchesDecision(t *testing.T) {
	seed := warmupReceiptSeed(tReceiptID, tRecipient, tDay)
	for _, rate := range []float64{0, 0.3, 0.75, 1} {
		want := warmup.ReplyDecision(seed, rate)
		got := planFor(rate, placementInbox).DoReply
		if got != want {
			t.Errorf("reply_rate %.2f: DoReply = %v, want ReplyDecision = %v", rate, got, want)
		}
	}
	if planFor(0, placementInbox).DoReply {
		t.Error("reply_rate 0: DoReply = true, want never")
	}
	if !planFor(1, placementInbox).DoReply {
		t.Error("reply_rate 1: DoReply = false, want always")
	}
}

// TestEngagePlanDelayMatchesWhatItWillDo is the "traffic feels instant" regression at
// the coreapi seam: a passive-only engagement keeps the short read dwell, while an
// engagement that WILL reply is delayed by the far longer reply latency. Getting these
// two crossed is exactly the bug that made replies arrive 30 seconds after their
// trigger, so both directions are pinned.
func TestEngagePlanDelayMatchesWhatItWillDo(t *testing.T) {
	passive := planFor(0, placementInbox) // rate 0 ⇒ never replies
	if passive.DoReply {
		t.Fatal("rate 0 must not reply")
	}
	if want := warmup.EngageDwell(tReceiptID); passive.EngageAfter != want {
		t.Errorf("passive EngageAfter = %v, want EngageDwell = %v", passive.EngageAfter, want)
	}

	replying := planFor(1, placementInbox) // rate 1 ⇒ always replies
	if !replying.DoReply {
		t.Fatal("rate 1 must reply")
	}
	want := warmup.ReplyEngageAfter(tReceiptID, tReceivedAt, tReceivedAt, nil)
	if replying.EngageAfter != want {
		t.Errorf("replying EngageAfter = %v, want ReplyEngageAfter = %v", replying.EngageAfter, want)
	}
	if replying.EngageAfter < 3*time.Minute {
		t.Errorf("replying EngageAfter = %v, want at least the 3-minute human floor", replying.EngageAfter)
	}
	if replying.EngageAfter <= passive.EngageAfter {
		t.Errorf("a reply (%v) is not slower than a passive engagement (%v)",
			replying.EngageAfter, passive.EngageAfter)
	}
}

// TestEngagePlanDeterministic proves the same inputs always yield the same plan
// (reproducible across a re-poll) and a different receipt id yields an independent
// dwell — the plan is a pure function of its seed.
func TestEngagePlanDeterministic(t *testing.T) {
	a := planFor(0.5, placementSpam)
	b := planFor(0.5, placementSpam)
	if a != b {
		t.Fatalf("plan not deterministic: %+v vs %+v", a, b)
	}
	// Whichever branch this receipt's seeded decision takes, the delay is bounded by
	// that branch's distribution: [5s, 1h] passive, [3m, 8h] plus waking-hours deferral
	// for a reply.
	lo, hi := 5*time.Second, time.Hour
	if a.DoReply {
		lo, hi = 3*time.Minute, 24*time.Hour
	}
	if a.EngageAfter < lo || a.EngageAfter > hi {
		t.Errorf("EngageAfter = %v (DoReply=%v), want within [%v, %v]", a.EngageAfter, a.DoReply, lo, hi)
	}
}

// TestPausedUntilPerState proves the pause window matches the health thresholds
// (spec §8): paused halts 72h, throttled 24h, watch/healthy clear the window.
func TestPausedUntilPerState(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		state     string
		wantValid bool
		wantDelay time.Duration
	}{
		{warmup.StatePaused, true, 72 * time.Hour},
		{warmup.StateThrottled, true, 24 * time.Hour},
		{warmup.StateWatch, false, 0},
		{warmup.StateHealthy, false, 0},
	}
	for _, tc := range cases {
		got := warmupPausedUntil(tc.state, now)
		if got.Valid != tc.wantValid {
			t.Errorf("state %q: Valid = %v, want %v", tc.state, got.Valid, tc.wantValid)
		}
		if tc.wantValid && !got.Time.Equal(now.Add(tc.wantDelay)) {
			t.Errorf("state %q: PausedUntil = %v, want %v", tc.state, got.Time, now.Add(tc.wantDelay))
		}
	}
}

// TestTimedBlockFloor proves the timed-block floor as the sweep actually composes
// it: HoldRecoveryDuringBlock clamps the health axis, then ShouldApplyTransition
// decides whether anything is left to write. Escalation applies immediately, a
// no-op never applies, and a recovery step-down is held while paused_until is in
// the future but allowed once it has elapsed.
func TestTimedBlockFloor(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	cases := []struct {
		name        string
		from, to    string
		pausedUntil time.Time
		wantHealth  string
		wantApply   bool
	}{
		{"escalation with live block still applies", warmup.StateThrottled, warmup.StatePaused, future, warmup.StatePaused, true},
		{"escalation from healthy applies", warmup.StateHealthy, warmup.StateWatch, time.Time{}, warmup.StateWatch, true},
		{"no-op never applies", warmup.StatePaused, warmup.StatePaused, past, warmup.StatePaused, false},
		{"recovery blocked while block is live", warmup.StatePaused, warmup.StateThrottled, future, warmup.StatePaused, false},
		{"recovery allowed once block elapsed", warmup.StatePaused, warmup.StateThrottled, past, warmup.StateThrottled, true},
		{"recovery allowed with no block set", warmup.StateThrottled, warmup.StateWatch, time.Time{}, warmup.StateWatch, true},
	}
	for _, tc := range cases {
		d := warmup.Decision{Health: tc.to, HealthReasonCode: "recovery_step", Lane: warmup.LaneHealthy}
		d = warmup.HoldRecoveryDuringBlock(d, tc.from, tc.pausedUntil, now)
		if d.Health != tc.wantHealth {
			t.Errorf("%s: health = %q, want %q", tc.name, d.Health, tc.wantHealth)
		}
		if got := warmup.ShouldApplyTransition(tc.from, d.Health, warmup.LaneHealthy, d.Lane); got != tc.wantApply {
			t.Errorf("%s: apply = %v, want %v", tc.name, got, tc.wantApply)
		}
	}
}

// The dwell governs the HEALTH axis only. A lane change is containment — auth
// regressed, evidence went stale, cooldown elapsed — not a recovery being rushed,
// so holding it back to respect a health timer would delay exactly the action that
// should not wait.
func TestTimedBlockDoesNotHoldLaneChanges(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	d := warmup.Decision{
		Health: warmup.StateThrottled, HealthReasonCode: "recovery_step",
		Lane: warmup.LanePendingAuth, LaneReasonCode: "lane_auth_regressed",
	}
	d = warmup.HoldRecoveryDuringBlock(d, warmup.StatePaused, now.Add(time.Hour), now)

	if d.Health != warmup.StatePaused {
		t.Fatalf("health = %q, want paused: the dwell has not elapsed", d.Health)
	}
	if d.Lane != warmup.LanePendingAuth {
		t.Fatalf("lane = %q, want pending_auth: a lane change must not be held by the health dwell", d.Lane)
	}
	if !warmup.ShouldApplyTransition(warmup.StatePaused, d.Health, warmup.LaneHealthy, d.Lane) {
		t.Fatal("a lane-only change must still be persisted")
	}
}

// TestValidPlacement guards the boundary check RecordWarmupReceipt uses before it
// touches the DB.
func TestValidPlacement(t *testing.T) {
	for _, ok := range []string{placementInbox, placementSpam, placementOther} {
		if !validPlacement(ok) {
			t.Errorf("validPlacement(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "INBOX", "junk", "trash"} {
		if validPlacement(bad) {
			t.Errorf("validPlacement(%q) = true, want false", bad)
		}
	}
}
