package warmup

import (
	"testing"
	"time"
)

func TestEvaluateHealthRequiresFreshQualifiedEvidence(t *testing.T) {
	cases := []struct {
		name        string
		in          Signals
		state, code string
	}{
		{"no evidence is unknown", Signals{AuthPassing: true, EvidenceFresh: true, CurrentHealth: StateHealthy},
			StateUnknown, "placement_sample_insufficient"},
		{"small bad sample is still unknown", Signals{AuthPassing: true, EvidenceFresh: true, CurrentHealth: StateHealthy, Spam: 19},
			StateUnknown, "placement_sample_insufficient"},
		{"stale evidence is unknown even when plentiful", Signals{AuthPassing: true, CurrentHealth: StateHealthy, Inbox: 500},
			StateUnknown, "placement_sample_insufficient"},
		{"degraded state cannot recover without placement", Signals{AuthPassing: true, EvidenceFresh: true, CurrentHealth: StatePaused},
			StatePaused, "insufficient_evidence_to_recover"},
		{"clean qualified placement establishes health", Signals{AuthPassing: true, EvidenceFresh: true, CurrentHealth: StateUnknown, Inbox: 20},
			StateHealthy, "evidence_qualified"},
		{"qualified recovery is gradual", Signals{AuthPassing: true, EvidenceFresh: true, CurrentHealth: StatePaused, Inbox: 20},
			StateThrottled, "recovery_step"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateParticipant(tc.in, time.Now())
			if got.Health != tc.state || got.HealthReasonCode != tc.code {
				t.Fatalf("health = %q/%q, want %q/%q", got.Health, got.HealthReasonCode, tc.state, tc.code)
			}
		})
	}
}

// The defect this replaces: MinComplaintSamples=100 against a 0.3% pause threshold
// meant ONE complaint on 100 delivered sends computed as 1% and paused the mailbox
// for 72 hours. One FBL report is not evidence of a 0.3% rate. A Wilson lower bound
// makes the small sample honest without discarding it.
func TestComplaintRateUsesWilsonLowerBound(t *testing.T) {
	one := Signals{AuthPassing: true, EvidenceFresh: true, CurrentHealth: StateHealthy, Inbox: 20,
		CampaignDelivered: 1000, CampaignComplaints: 1}
	if d := EvaluateParticipant(one, time.Now()); d.Health != StateHealthy {
		t.Fatalf("one complaint in 1000 gave %q (%s); a single report is not evidence of a 0.3%% rate", d.Health, d.HealthReasonCode)
	}

	// Below the minimum the rate is UNPROVEN, not clean and not damning: 5 complaints
	// in 100 is a 5% point estimate, but 100 sends cannot resolve a 0.3% threshold.
	tiny := Signals{AuthPassing: true, EvidenceFresh: true, CurrentHealth: StateHealthy, Inbox: 20,
		CampaignDelivered: 100, CampaignComplaints: 5}
	if d := EvaluateParticipant(tiny, time.Now()); d.Health != StateHealthy {
		t.Fatalf("5 complaints in 100 gave %q (%s); a sub-minimum sample must not be actionable", d.Health, d.HealthReasonCode)
	}

	many := Signals{AuthPassing: true, EvidenceFresh: true, CurrentHealth: StateHealthy, Inbox: 20,
		CampaignDelivered: 1000, CampaignComplaints: 20}
	if d := EvaluateParticipant(many, time.Now()); d.Health != StatePaused {
		t.Fatalf("20 complaints in 1000 gave %q, want paused — 2%% is far above the 0.3%% threshold", d.Health)
	}
}

// The defect this replaces: bounce_samples summed campaign and warmup sends, and
// warmup traffic (which essentially never hard-bounces) diluted the denominator
// below the thresholds it was meant to trip. A real 10% campaign bounce rate read
// as 1.4% and the mailbox looked healthy.
func TestBounceDenominatorsAreNotPooled(t *testing.T) {
	s := Signals{
		AuthPassing: true, EvidenceFresh: true, CurrentHealth: StateHealthy, Inbox: 20,
		CampaignDelivered: 200, CampaignHardBounces: 20, // a true 10% rate
		WarmupDelivered: 1200, WarmupHardBounces: 0, // clean synthetic traffic
	}
	d := EvaluateParticipant(s, time.Now())
	// A 10% observed rate on 200 sends bounds to ~6.6%, which clears the 5% throttle
	// band but not the 10% pause band — Wilson deliberately will not pause on a
	// sample that loose. What matters is that it degrades AT ALL: Phase 0 pooled the
	// denominators, computed 1.4%, and left this mailbox looking healthy.
	if stateRank(d.Health) < stateRank(StateThrottled) {
		t.Fatalf("health = %q (%s), want throttled or worse: 20/200 campaign bounces is 10%%, and 1200 clean warmup sends must not dilute it",
			d.Health, d.HealthReasonCode)
	}
	if d.HealthReasonCode != "campaign_bounce_throttle" {
		t.Fatalf("reason = %q, want the campaign bounce band to be the driver", d.HealthReasonCode)
	}
	if d.CampaignBounceSamples != 200 || d.WarmupBounceSamples != 1200 {
		t.Fatalf("populations were pooled: campaign=%d warmup=%d, want 200 and 1200 kept apart",
			d.CampaignBounceSamples, d.WarmupBounceSamples)
	}
}

func TestLaneTransitions(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		in   Signals
		lane string
		code string
	}{
		{"unauthenticated domain cannot start", Signals{EvidenceFresh: true, CurrentLane: LanePendingAuth},
			LanePendingAuth, "lane_pending_auth"},
		{"auth passing admits to probation, never straight to healthy",
			Signals{AuthPassing: true, EvidenceFresh: true, CurrentLane: LanePendingAuth, Inbox: 20},
			LaneProbation, "lane_admitted_to_probation"},
		{"auth regression pulls a healthy mailbox out of the pool",
			Signals{AuthPassing: false, EvidenceFresh: true, CurrentLane: LaneHealthy, Inbox: 20},
			LanePendingAuth, "lane_auth_regressed"},
		{"probation qualifies into the healthy pool",
			Signals{AuthPassing: true, EvidenceFresh: true, CurrentLane: LaneProbation, Inbox: 20},
			LaneHealthy, "lane_qualified"},
		{"probation without evidence stays put",
			Signals{AuthPassing: true, EvidenceFresh: true, CurrentLane: LaneProbation},
			LaneProbation, "lane_awaiting_evidence"},
		{"degraded health quarantines from healthy",
			Signals{AuthPassing: true, EvidenceFresh: true, CurrentLane: LaneHealthy, Inbox: 4, Spam: 16},
			LaneQuarantine, "lane_quarantined"},
		{"quarantine holds while the cooldown runs",
			Signals{AuthPassing: true, EvidenceFresh: true, CurrentLane: LaneQuarantine, Inbox: 20,
				QuarantinedSince: now.Add(-time.Hour)},
			LaneQuarantine, "lane_cooldown_active"},
		{"elapsed cooldown enters recovery, not healthy",
			Signals{AuthPassing: true, EvidenceFresh: true, CurrentLane: LaneQuarantine, Inbox: 20,
				QuarantinedSince: now.Add(-96 * time.Hour)},
			LaneRecovery, "lane_cooldown_elapsed"},
		{"recovery must requalify to rejoin healthy",
			Signals{AuthPassing: true, EvidenceFresh: true, CurrentLane: LaneRecovery, Inbox: 20},
			LaneHealthy, "lane_qualified"},
		{"blocked is never moved by policy",
			Signals{AuthPassing: true, EvidenceFresh: true, CurrentLane: LaneBlocked, Inbox: 20},
			LaneBlocked, "lane_blocked_held"},
		{"a fresh evidence lapse holds the healthy lane while the grace runs",
			Signals{AuthPassing: true, CurrentLane: LaneHealthy, CurrentHealth: StateHealthy, Inbox: 20},
			LaneHealthy, "lane_evidence_grace"},
		{"a lapse that outlasts the grace returns a healthy mailbox to probation",
			Signals{AuthPassing: true, CurrentLane: LaneHealthy, CurrentHealth: StateUnknown, Inbox: 20,
				EvidenceLapsedSince: now.Add(-LaneEvidenceGrace - time.Minute)},
			LaneProbation, "lane_evidence_lapsed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateParticipant(tc.in, now)
			if got.Lane != tc.lane {
				t.Fatalf("lane = %q (%s), want %q", got.Lane, got.LaneReasonCode, tc.lane)
			}
			if tc.code != "" && got.LaneReasonCode != tc.code {
				t.Fatalf("lane reason = %q, want %q", got.LaneReasonCode, tc.code)
			}
		})
	}
}

// Acceptance criterion 1: a healthy customer mailbox never exchanges warmup traffic
// with a participant that has not earned the healthy lane.
func TestLaneCompatibilityIsolatesHealthyPeers(t *testing.T) {
	all := []string{LanePendingAuth, LaneProbation, LaneHealthy, LaneWatch, LaneRecovery, LaneQuarantine, LaneBlocked}
	for _, other := range all {
		if other == LaneHealthy {
			continue
		}
		if LanesCompatible(LaneHealthy, other) || LanesCompatible(other, LaneHealthy) {
			t.Errorf("lane %q can exchange traffic with healthy", other)
		}
	}
	if !LanesCompatible(LaneHealthy, LaneHealthy) {
		t.Error("healthy peers must be able to exchange traffic")
	}
	for _, sealed := range []string{LanePendingAuth, LaneQuarantine, LaneBlocked} {
		if LanesCompatible(sealed, sealed) {
			t.Errorf("lane %q must not send even to its own lane", sealed)
		}
	}
}

func TestLaneVolumeAndLeadGating(t *testing.T) {
	if got := LaneDailyVolume(LaneProbation, 40); got != ProbationDailyVolume {
		t.Fatalf("probation volume = %d, want %d", got, ProbationDailyVolume)
	}
	if got := LaneDailyVolume(LaneProbation, 3); got != 3 {
		t.Fatalf("probation must not RAISE a lower ramp target, got %d, want 3", got)
	}
	if got := LaneDailyVolume(LaneHealthy, 40); got != 40 {
		t.Fatalf("healthy volume = %d, want the full ramp target 40", got)
	}
	for _, sealed := range []string{LanePendingAuth, LaneQuarantine, LaneBlocked} {
		if got := LaneDailyVolume(sealed, 40); got != 0 {
			t.Errorf("lane %q volume = %d, want 0", sealed, got)
		}
		if LaneMayTakeNewLead(sealed) {
			t.Errorf("lane %q must not take a new campaign lead", sealed)
		}
	}
	for _, sending := range []string{LaneHealthy, LaneWatch, LaneProbation, LaneRecovery} {
		if !LaneMayTakeNewLead(sending) {
			t.Errorf("lane %q should still take new leads, at reduced volume", sending)
		}
	}
}

// An unrecognized lane can only come from a direct write. It must fail SAFE —
// bounded volume, its own lane, no access to healthy peers — rather than default
// into the pool.
func TestUnknownLaneFailsSafe(t *testing.T) {
	if LanesCompatible("nonsense", LaneHealthy) {
		t.Error("an unrecognized lane must not reach healthy peers")
	}
	if got := LaneDailyVolume("nonsense", 40); got != ProbationDailyVolume {
		t.Fatalf("unrecognized lane volume = %d, want the probation cap %d", got, ProbationDailyVolume)
	}
}

// A health recovery is held by the timed block, but a lane change is not: the
// reasons a lane must move (auth regressed, cooldown elapsed, evidence stale) are
// not recoveries being rushed, and delaying containment for a dwell timer would be
// backwards.
func TestRecoveryDwellHoldsHealthButNotLane(t *testing.T) {
	now := time.Now()
	d := EvaluateParticipant(Signals{AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StatePaused, CurrentLane: LaneQuarantine, Inbox: 20,
		QuarantinedSince: now.Add(-96 * time.Hour)}, now)

	held := HoldRecoveryDuringBlock(d, StatePaused, now.Add(time.Hour), now)
	if held.Health != StatePaused {
		t.Fatalf("health = %q, want paused: the dwell has not elapsed", held.Health)
	}
	if held.HealthReasonCode != "recovery_blocked_by_dwell" {
		t.Fatalf("health reason = %q, want recovery_blocked_by_dwell", held.HealthReasonCode)
	}

	elapsed := HoldRecoveryDuringBlock(d, StatePaused, now.Add(-time.Hour), now)
	if elapsed.Health != StateThrottled {
		t.Fatalf("health = %q, want throttled once the dwell elapses", elapsed.Health)
	}
}

// Every decision the evaluator asks to persist must name itself. The transitions
// table enforces reason_code <> ” and shares one atomic statement with the
// participant update, so an unexplained decision does not merely lose its audit
// trail — it aborts the state change and the mailbox never moves. Sweeps the whole
// surface rather than the pairs that regressed.
func TestEveryAppliedTransitionIsExplained(t *testing.T) {
	now := time.Now()
	healths := []string{StateUnknown, StateHealthy, StateWatch, StateThrottled, StatePaused}
	lanes := []string{LanePendingAuth, LaneProbation, LaneHealthy, LaneWatch, LaneRecovery, LaneQuarantine, LaneBlocked}
	shapes := []struct {
		name string
		in   Signals
	}{
		{"no evidence", Signals{}},
		{"clean qualified", Signals{EvidenceFresh: true, Inbox: 20}},
		{"spam watch", Signals{EvidenceFresh: true, Inbox: 13, Spam: 7}},
		{"spam pause", Signals{EvidenceFresh: true, Inbox: 4, Spam: 16}},
		{"campaign bounces", Signals{EvidenceFresh: true, Inbox: 20, CampaignDelivered: 200, CampaignHardBounces: 20}},
		{"complaints", Signals{EvidenceFresh: true, Inbox: 20, CampaignDelivered: 1000, CampaignComplaints: 20}},
		{"stale", Signals{Inbox: 500}},
	}
	for _, h := range healths {
		for _, l := range lanes {
			for _, auth := range []bool{true, false} {
				for _, sh := range shapes {
					in := sh.in
					in.CurrentHealth, in.CurrentLane, in.AuthPassing = h, l, auth
					d := EvaluateParticipant(in, now)
					if !ShouldApplyTransition(h, d.Health, l, d.Lane) {
						continue // nothing written, nothing to explain
					}
					// Asserted on the WRITE, not on whether this axis moved. The health
					// reason_code column is NOT NULL CHECK (btrim <> '') and shares one
					// atomic statement with the lane update, so an unexplained health
					// decision aborts the lane change travelling with it.
					if d.HealthReasonCode == "" || d.HealthReason == "" {
						t.Fatalf("%s/%s auth=%v %s: transition to %s/%s persisted with no health explanation",
							h, l, auth, sh.name, d.Health, d.Lane)
					}
					if normalizeLane(d.Lane) != normalizeLane(l) && (d.LaneReasonCode == "" || d.LaneReason == "") {
						t.Fatalf("%s/%s auth=%v %s: lane %s -> %s persisted with no explanation",
							h, l, auth, sh.name, l, d.Lane)
					}
				}
			}
		}
	}
}

// A lane-only promotion (health already healthy and unchanged) still writes a row
// into a table whose reason_code is NOT NULL CHECK (btrim <> ”), in the SAME
// atomic statement as the participant update. An empty code aborted both, so the
// mailbox was never promoted and the sweep re-failed identically forever.
func TestLaneOnlyTransitionStillNamesTheHealthAxis(t *testing.T) {
	now := time.Now()
	d := EvaluateParticipant(Signals{
		AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StateHealthy, CurrentLane: LaneProbation, Inbox: 20,
	}, now)

	if d.Lane != LaneHealthy || d.Health != StateHealthy {
		t.Fatalf("got %s/%s, want healthy/healthy", d.Health, d.Lane)
	}
	if !ShouldApplyTransition(StateHealthy, d.Health, LaneProbation, d.Lane) {
		t.Fatal("a lane promotion must be persisted")
	}
	if d.HealthReasonCode == "" {
		t.Fatal("the health axis must name itself even when unchanged; an empty reason_code aborts the whole statement")
	}
}

// Quarantine is stricter than pending_auth: both seal the mailbox, but only
// quarantine carries a cooldown. A DNS blip must not launder containment into
// probation, which may send and may take new campaign leads.
func TestAuthRegressionCannotLaunderAQuarantine(t *testing.T) {
	now := time.Now()
	entered := now.Add(-time.Hour) // well inside the 72h cooldown

	regressed := EvaluateParticipant(Signals{
		AuthPassing: false, EvidenceFresh: true, CurrentLane: LaneQuarantine,
		Inbox: 20, QuarantinedSince: entered,
	}, now)
	if regressed.Lane != LaneQuarantine {
		t.Fatalf("auth regression moved a quarantined mailbox to %q; quarantine is the stricter state", regressed.Lane)
	}

	// Even reached from pending_auth, an unexpired quarantine must resume.
	restored := EvaluateParticipant(Signals{
		AuthPassing: true, EvidenceFresh: true, CurrentLane: LanePendingAuth,
		Inbox: 20, QuarantinedSince: entered,
	}, now)
	if restored.Lane != LaneQuarantine {
		t.Fatalf("lane = %q, want quarantine: the cooldown has not elapsed", restored.Lane)
	}
	if LaneMayTakeNewLead(restored.Lane) || LaneMaySend(restored.Lane) {
		t.Fatal("a resumed quarantine must neither send nor take new leads")
	}
}

// A thin but catastrophic arm must not be promoted. Escalation needs a minimum
// sample so thin evidence cannot produce false positives; vouching for a mailbox
// does not get the same benefit of the doubt.
func TestUnprovenBadEvidenceBlocksPromotion(t *testing.T) {
	now := time.Now()
	d := EvaluateParticipant(Signals{
		AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StateHealthy, CurrentLane: LaneProbation, Inbox: 20,
		CampaignDelivered: 49, CampaignHardBounces: 25, // 51% on real recipients
	}, now)

	if d.Lane == LaneHealthy {
		t.Fatal("25 hard bounces on 49 recipients must not join the healthy pool, even below MinBounceSamples")
	}
	// It is below the minimum sample, so it must not be PUNISHED either.
	if d.Health != StateHealthy {
		t.Fatalf("health = %q: a sub-minimum sample must not escalate", d.Health)
	}
}

// A genuinely empty arm is no evidence and must not block promotion.
func TestEmptyArmsDoNotBlockPromotion(t *testing.T) {
	d := EvaluateParticipant(Signals{
		AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StateHealthy, CurrentLane: LaneProbation, Inbox: 20,
	}, time.Now())
	if d.Lane != LaneHealthy {
		t.Fatalf("lane = %q (%s), want healthy: no campaign traffic at all is not bad evidence", d.Lane, d.LaneReasonCode)
	}
}

// The dwell holds a health recovery, so it must also hold the lane promotion that
// recovery would justify — otherwise a throttled mailbox lands in the recovery lane
// and may send, which is the mirror of the "quarantined but healthy" pair the
// atomic write exists to prevent.
func TestDwellHoldsTheLanePromotionToo(t *testing.T) {
	now := time.Now()
	d := EvaluateParticipant(Signals{
		AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StateThrottled, CurrentLane: LaneQuarantine, Inbox: 20,
		QuarantinedSince: now.Add(-96 * time.Hour),
		PausedUntil:      now.Add(time.Hour), // block still in force
	}, now)

	if d.Health != StateThrottled {
		t.Fatalf("health = %q, want throttled: the dwell has not elapsed", d.Health)
	}
	if LaneMaySend(d.Lane) {
		t.Fatalf("lane = %q may send, but the health recovery that would justify it was refused", d.Lane)
	}
}

// A single unqualified tick used to cost a healthy mailbox its lane: it demoted
// on one sweep and was promoted back on the next, five minutes later, leaving two
// audit rows and a day capped at ProbationDailyVolume for a mailbox nothing was
// ever wrong with. This walks that exact sequence and asserts the lane never
// moves.
func TestASingleEvidenceDipDoesNotCostTheHealthyLane(t *testing.T) {
	now := time.Now()
	qualified := Signals{
		AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StateHealthy, CurrentLane: LaneHealthy, Inbox: 20,
	}

	// Tick 1: the window slides past the twentieth placement. Health drops to
	// unknown (it is genuinely unmeasured), but the LANE holds.
	dip := qualified
	dip.Inbox = 19
	dipped := EvaluateParticipant(dip, now)
	if dipped.Health != StateUnknown {
		t.Fatalf("health = %q, want unknown: a thin sample is still unmeasured", dipped.Health)
	}
	if dipped.Lane != LaneHealthy || dipped.LaneReasonCode != "lane_evidence_grace" {
		t.Fatalf("lane = %q (%s), want healthy held by the grace", dipped.Lane, dipped.LaneReasonCode)
	}

	// Tick 2, five minutes later: evidence is back. The mailbox requalifies from
	// the lane it never left, so nothing about the pool changed.
	back := qualified
	back.CurrentHealth = StateUnknown
	back.EvidenceLapsedSince = now
	recovered := EvaluateParticipant(back, now.Add(5*time.Minute))
	if recovered.Lane != LaneHealthy {
		t.Fatalf("lane = %q, want healthy", recovered.Lane)
	}
	if recovered.Health != StateHealthy {
		t.Fatalf("health = %q, want healthy", recovered.Health)
	}
}

// The grace is a delay, not an exemption: a lapse that persists past it demotes.
func TestAPersistentEvidenceLapseStillDemotes(t *testing.T) {
	now := time.Now()
	in := Signals{
		AuthPassing: true, CurrentHealth: StateUnknown, CurrentLane: LaneHealthy, Inbox: 19,
		EvidenceLapsedSince: now.Add(-LaneEvidenceGrace - time.Second),
	}
	d := EvaluateParticipant(in, now)
	if d.Lane != LaneProbation || d.LaneReasonCode != "lane_evidence_lapsed" {
		t.Fatalf("lane = %q (%s), want probation/lane_evidence_lapsed", d.Lane, d.LaneReasonCode)
	}

	// One second earlier it is still inside the grace — the boundary is the rule,
	// not an accident of rounding.
	in.EvidenceLapsedSince = now.Add(-LaneEvidenceGrace + time.Second)
	if held := EvaluateParticipant(in, now); held.Lane != LaneHealthy {
		t.Fatalf("inside the grace: lane = %q, want healthy", held.Lane)
	}
}

// An unmeasured mailbox with no recorded fall — one backfilled straight into the
// healthy lane, for instance — has no lapse to date the grace from and must not
// be held indefinitely. An unbounded hold is the failure the grace bounds, not
// one it may create.
func TestUnknownHealthWithNoRecordedLapseIsNotHeld(t *testing.T) {
	d := EvaluateParticipant(Signals{
		AuthPassing: true, CurrentHealth: StateUnknown, CurrentLane: LaneHealthy,
	}, time.Now())
	if d.Lane != LaneProbation || d.LaneReasonCode != "lane_evidence_lapsed" {
		t.Fatalf("lane = %q (%s), want probation/lane_evidence_lapsed", d.Lane, d.LaneReasonCode)
	}
}

// The grace must never slow CONTAINMENT. Everything that means "something is
// wrong" — a degraded rate, an auth regression, a small pile of bad evidence —
// still moves the mailbox out of the healthy lane on the first sweep, with no
// lapse marker anywhere in sight.
func TestContainmentIsNeverDelayedByTheEvidenceGrace(t *testing.T) {
	now := time.Now()
	healthy := Signals{
		AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StateHealthy, CurrentLane: LaneHealthy,
	}
	cases := []struct {
		name string
		in   Signals
		lane string
	}{
		{"spam pause quarantines immediately", withSignals(healthy, func(s *Signals) {
			s.Inbox, s.Spam = 4, 16
		}), LaneQuarantine},
		{"spam watch leaves the healthy lane immediately", withSignals(healthy, func(s *Signals) {
			s.Inbox, s.Spam = 12, 8
		}), LaneWatch},
		{"campaign hard bounces quarantine immediately", withSignals(healthy, func(s *Signals) {
			s.Inbox, s.CampaignDelivered, s.CampaignHardBounces = 20, 200, 40
		}), LaneQuarantine},
		{"an auth regression withdraws the mailbox immediately", withSignals(healthy, func(s *Signals) {
			s.AuthPassing, s.Inbox = false, 20
		}), LanePendingAuth},
		{"a thin pile of BAD evidence is a signal, not a blind spot", withSignals(healthy, func(s *Signals) {
			// Below MinBounceSamples, so it cannot degrade health — but it is
			// evidence of something wrong, and promotionAlarmed refuses to vouch.
			s.Inbox, s.CampaignDelivered, s.CampaignHardBounces = 20, 40, 20
		}), LaneProbation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateParticipant(tc.in, now); got.Lane != tc.lane {
				t.Fatalf("lane = %q (%s), want %q on the FIRST sweep", got.Lane, got.LaneReasonCode, tc.lane)
			}
		})
	}
}

// withSignals copies a base Signals and applies one mutation, so each case above
// states only what it changes.
func withSignals(base Signals, mutate func(*Signals)) Signals {
	mutate(&base)
	return base
}

// New campaign leads are gated on the mailbox AND its organizational domain
// (design §7). Only containment withholds: pending_auth is an advisory DNS
// verdict (security.md invariant 39) and must never be able to stop a campaign,
// and an empty lane means the mailbox is not warming up at all.
func TestNewLeadsWithheldGatesMailboxAndDomain(t *testing.T) {
	cases := []struct {
		name         string
		mailbox      string
		domain       string
		wantWithheld bool
	}{
		{"not warming up at all", "", "", false},
		{"both healthy", LaneHealthy, LaneHealthy, false},
		{"own quarantine", LaneQuarantine, LaneQuarantine, true},
		{"own blocked", LaneBlocked, LaneBlocked, true},
		{"a quarantined sibling withholds a healthy mailbox", LaneHealthy, LaneQuarantine, true},
		{"a blocked sibling withholds a healthy mailbox", LaneHealthy, LaneBlocked, true},
		{"a quarantined sibling withholds a mailbox not warming up", "", LaneQuarantine, true},
		{"a probation sibling does not", LaneHealthy, LaneProbation, false},
		{"a watch sibling does not", LaneHealthy, LaneWatch, false},
		{"a recovery sibling does not", LaneHealthy, LaneRecovery, false},
		{"pending_auth is advisory on the mailbox", LanePendingAuth, LanePendingAuth, false},
		{"pending_auth is advisory on the domain too", LaneHealthy, LanePendingAuth, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewLeadsWithheld(tc.mailbox, tc.domain); got != tc.wantWithheld {
				t.Fatalf("NewLeadsWithheld(%q, %q) = %v, want %v", tc.mailbox, tc.domain, got, tc.wantWithheld)
			}
		})
	}
}

// deliverability:write exists so an ingest credential cannot mutate campaigns.
// Pooling feed-reported bounces with self-observed ones handed it exactly that:
// ~7 forged events reached throttled -> degraded -> quarantine, which withholds
// the mailbox's whole domain for 72h and which the tenant cannot clear. Asserted
// evidence may reduce volume; it may not contain.
func TestAssertedBouncesAdviseButCannotContain(t *testing.T) {
	now := time.Now()
	base := Signals{
		AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StateHealthy, CurrentLane: LaneHealthy, Inbox: 20,
		CampaignDelivered: 200,
	}

	asserted := base
	asserted.CampaignAssertedHardBounces = 60 // 30% reported — far past the pause band
	d := EvaluateParticipant(asserted, now)
	if stateRank(d.Health) > stateRank(StateWatch) {
		t.Fatalf("health = %q (%s): feed-reported evidence must not exceed watch", d.Health, d.HealthReasonCode)
	}
	if LaneMaySend(d.Lane) == false || d.Lane == LaneQuarantine {
		t.Fatalf("lane = %q: an ingest credential must not be able to contain a mailbox", d.Lane)
	}

	// The identical rate, self-observed, still contains — the cap is about the
	// evidence's authority, not about tolerating bounces.
	observed := base
	observed.CampaignHardBounces = 60
	d = EvaluateParticipant(observed, now)
	if stateRank(d.Health) < stateRank(StateThrottled) {
		t.Fatalf("health = %q: self-observed bounces at the same rate must still contain", d.Health)
	}
	if d.Lane != LaneQuarantine {
		t.Fatalf("lane = %q, want quarantine for self-observed evidence", d.Lane)
	}
}

// Advisory-for-containment is not the same as vouching: a mailbox the feed says is
// bouncing must not be promoted into the healthy pool just because the evidence
// cannot punish it.
func TestAssertedBouncesStillBlockPromotion(t *testing.T) {
	d := EvaluateParticipant(Signals{
		AuthPassing: true, EvidenceFresh: true,
		CurrentHealth: StateHealthy, CurrentLane: LaneProbation, Inbox: 20,
		CampaignDelivered: 200, CampaignAssertedHardBounces: 60,
	}, time.Now())
	if d.Lane == LaneHealthy {
		t.Fatal("a mailbox the feed reports as heavily bouncing must not join the healthy pool")
	}
}
