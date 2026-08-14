package inprocess

import (
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/sendcap"
)

// withHealth stamps a warmup health state onto a candidate row.
func withHealth(r gen.ListCampaignSenderCandidatesRow, state string) gen.ListCampaignSenderCandidatesRow {
	r.HealthState = state
	return r
}

// The half of health gating that decides who may take a new contact: a paused
// member is excluded outright (like a disabled row), and a degraded one keeps a
// smaller share of its cap.
func TestEligibleCandidatesGatesOnWarmupHealth(t *testing.T) {
	healthy, watch, throttled, paused := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	got := eligibleCandidates([]gen.ListCampaignSenderCandidatesRow{
		candidateRow(healthy, 1, true, mailboxStatusActive, 100, 0),
		withHealth(candidateRow(watch, 1, true, mailboxStatusActive, 100, 0), sendcap.HealthWatch),
		withHealth(candidateRow(throttled, 1, true, mailboxStatusActive, 100, 0), sendcap.HealthThrottled),
		withHealth(candidateRow(paused, 1, true, mailboxStatusActive, 100, 0), sendcap.HealthPaused),
	}, noDomainLanes)
	remaining := map[string]int{}
	for _, c := range got {
		remaining[c.MailboxID] = c.RemainingToday
	}
	if _, ok := remaining[paused.String()]; ok {
		t.Error("a paused mailbox is eligible for cold sending; it must be excluded entirely")
	}
	for id, want := range map[uuid.UUID]int{healthy: 100, watch: 70, throttled: 50} {
		if got := remaining[id.String()]; got != want {
			t.Errorf("mailbox %s remaining = %d, want %d", id, got, want)
		}
	}
}

// A degraded mailbox stops at its SCALED cap, not its full one: gating that only
// lowered the selector's score would let it keep sending to the real ceiling.
func TestEligibleCandidatesDropsADegradedMailboxAtItsScaledCap(t *testing.T) {
	id := uuid.New()
	// Ramped cap 100, throttled to 50, and it has already sent 50 today.
	got := eligibleCandidates([]gen.ListCampaignSenderCandidatesRow{
		withHealth(candidateRow(id, 1, true, mailboxStatusActive, 100, 50), sendcap.HealthThrottled),
	}, noDomainLanes)
	if len(got) != 0 {
		t.Errorf("eligible = %+v, want none: 50 sent already fills a throttled cap of 50", got)
	}
}

// Invariant 3, at the unit level: a pool with nothing eligible must produce the
// existing cap-DEFERRAL verdict (sent >= cap > 0), never the degenerate-cap verdict
// (cap <= 0) that STOPS the enrollment. A paused mailbox may recover, and the thread
// cannot be re-routed to another mailbox, so it has to wait.
func TestExhaustedPoolDefersRatherThanStopping(t *testing.T) {
	paused := withHealth(candidateRow(uuid.New(), 1, true, mailboxStatusActive, 100, 0), sendcap.HealthPaused)
	s, err := client{}.exhaustedPoolSender(gen.GetStepEnrollmentBundleRow{}, []gen.ListCampaignSenderCandidatesRow{paused}, noDomainLanes)
	if err != nil {
		t.Fatalf("exhaustedPoolSender: %v", err)
	}
	if !s.healthPaused {
		t.Error("healthPaused = false for a pool whose only member is paused")
	}
	if s.effectiveCap != 100 {
		t.Errorf("effectiveCap = %d, want the pool's real capacity of 100 (a reported number must be true)", s.effectiveCap)
	}
	if s.sentToday < s.effectiveCap {
		t.Errorf("cap/sent = %d/%d, want sent >= cap so the worker defers", s.effectiveCap, s.sentToday)
	}
}

// The degenerate case the aggregate numbers alone cannot express: a paused mailbox
// whose cap is zero reports zero pool capacity, which is the branch that stops an
// enrollment. The explicit flag is what keeps it a deferral.
func TestExhaustedPoolFlagsAPausedMailboxWithNoCap(t *testing.T) {
	paused := withHealth(candidateRow(uuid.New(), 1, true, mailboxStatusActive, 0, 0), sendcap.HealthPaused)
	s, err := client{}.exhaustedPoolSender(gen.GetStepEnrollmentBundleRow{}, []gen.ListCampaignSenderCandidatesRow{paused}, noDomainLanes)
	if err != nil {
		t.Fatalf("exhaustedPoolSender: %v", err)
	}
	if s.effectiveCap != 0 {
		t.Fatalf("effectiveCap = %d, want 0 for this fixture", s.effectiveCap)
	}
	if !s.healthPaused {
		t.Error("a zero-cap paused mailbox must still defer, not reach the degenerate-cap stop")
	}
}

// A pool exhausted for ordinary reasons must NOT claim a health pause — the worker
// logs the difference, and a mis-set cap is not a warmup verdict.
func TestExhaustedPoolDoesNotClaimHealthWhenMerelyCapped(t *testing.T) {
	rows := []gen.ListCampaignSenderCandidatesRow{
		candidateRow(uuid.New(), 1, true, mailboxStatusActive, 40, 40), // at cap
		candidateRow(uuid.New(), 1, false, mailboxStatusActive, 60, 0), // disabled
	}
	s, err := client{}.exhaustedPoolSender(gen.GetStepEnrollmentBundleRow{}, rows, noDomainLanes)
	if err != nil {
		t.Fatalf("exhaustedPoolSender: %v", err)
	}
	if s.healthPaused {
		t.Error("healthPaused = true for a pool that is merely capped/disabled")
	}
	if s.effectiveCap != 100 || s.sentToday != 100 {
		t.Errorf("cap/sent = %d/%d, want 100/100 (a disabled member's whole cap is unavailable)",
			s.effectiveCap, s.sentToday)
	}
}
