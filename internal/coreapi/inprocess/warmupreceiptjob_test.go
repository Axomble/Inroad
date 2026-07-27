package inprocess

import (
	"testing"
	"time"

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
		plan := warmupEngagePlan(tReceiptID, tRecipient, tDay, 0.3, tc.placement)
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
		got := warmupEngagePlan(tReceiptID, tRecipient, tDay, rate, placementInbox).DoReply
		if got != want {
			t.Errorf("reply_rate %.2f: DoReply = %v, want ReplyDecision = %v", rate, got, want)
		}
	}
	if warmupEngagePlan(tReceiptID, tRecipient, tDay, 0, placementInbox).DoReply {
		t.Error("reply_rate 0: DoReply = true, want never")
	}
	if !warmupEngagePlan(tReceiptID, tRecipient, tDay, 1, placementInbox).DoReply {
		t.Error("reply_rate 1: DoReply = false, want always")
	}
}

// TestEngagePlanDeterministic proves the same inputs always yield the same plan
// (reproducible across a re-poll) and a different receipt id yields an independent
// dwell — the plan is a pure function of its seed.
func TestEngagePlanDeterministic(t *testing.T) {
	a := warmupEngagePlan(tReceiptID, tRecipient, tDay, 0.5, placementSpam)
	b := warmupEngagePlan(tReceiptID, tRecipient, tDay, 0.5, placementSpam)
	if a != b {
		t.Fatalf("plan not deterministic: %+v vs %+v", a, b)
	}
	if a.EngageAfter != warmup.EngageDwell(tReceiptID) {
		t.Errorf("EngageAfter = %v, want EngageDwell(receipt) = %v", a.EngageAfter, warmup.EngageDwell(tReceiptID))
	}
	if a.EngageAfter < 5*time.Second || a.EngageAfter > time.Hour {
		t.Errorf("EngageAfter = %v, want within [5s, 1h]", a.EngageAfter)
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

// TestShouldApplyTransitionFloor proves the timed-block floor: escalation applies
// immediately, a no-op never applies, and a recovery step-down is held back while
// paused_until is still in the future but allowed once it has elapsed.
func TestShouldApplyTransitionFloor(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	cases := []struct {
		name        string
		from, to    string
		pausedUntil time.Time
		want        bool
	}{
		{"escalation with live block still applies", warmup.StateThrottled, warmup.StatePaused, future, true},
		{"escalation from healthy applies", warmup.StateHealthy, warmup.StateWatch, time.Time{}, true},
		{"no-op never applies", warmup.StatePaused, warmup.StatePaused, past, false},
		{"recovery blocked while block is live", warmup.StatePaused, warmup.StateThrottled, future, false},
		{"recovery allowed once block elapsed", warmup.StatePaused, warmup.StateThrottled, past, true},
		{"recovery allowed with no block set", warmup.StateThrottled, warmup.StateWatch, time.Time{}, true},
	}
	for _, tc := range cases {
		if got := warmup.ShouldApplyTransition(tc.from, tc.to, tc.pausedUntil, now); got != tc.want {
			t.Errorf("%s: ShouldApplyTransition(%q,%q) = %v, want %v", tc.name, tc.from, tc.to, got, tc.want)
		}
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
