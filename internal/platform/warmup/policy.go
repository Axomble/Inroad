package warmup

import (
	"math"
	"time"
)

// Pool lanes. These exact strings are pinned by migration 000055's CHECK
// constraint on warmup_participants.lane.
//
// A lane answers "who may this mailbox exchange traffic with". It is a SEPARATE
// axis from health_state, which answers "how does this mailbox's outbound mail
// perform". Phase 0 overloaded one column with both questions, so a mailbox with
// no evidence and a mailbox with bad evidence looked identical to the pool.
const (
	LanePendingAuth = "pending_auth"
	LaneProbation   = "probation"
	LaneHealthy     = "healthy"
	LaneWatch       = "watch"
	LaneRecovery    = "recovery"
	LaneQuarantine  = "quarantine"
	LaneBlocked     = "blocked"
)

// Bounce populations. These exact strings are pinned by migration 000058's CHECK
// constraint on warmup_state_transitions.bounce_population.
//
// The two are kept apart deliberately: pooling them let synthetic warmup traffic
// dilute a real campaign bounce rate below its own threshold (see
// TestBounceDenominatorsAreNotPooled). A transition therefore records ONE pair —
// whichever arm drove the decision — and has to say which.
const (
	BouncePopulationCampaign = "campaign"
	BouncePopulationWarmup   = "warmup"
)

// PolicyVersion stamps every transition so a decision stays explainable after the
// thresholds move. Bump it whenever a threshold or rule below changes.
const PolicyVersion = "warmup-phase1-v1"

// Health states, ordered from best to worst, pinned by migration 000054's CHECK
// constraint on warmup_participants.health_state.
const (
	StateHealthy   = "healthy"
	StateUnknown   = "unknown"
	StateWatch     = "watch"
	StateThrottled = "throttled"
	StatePaused    = "paused"
)

// Rate thresholds. Each is compared against a Wilson lower bound, never a point
// estimate — see qualifiedRate.
const (
	spamWatchRate         = 0.15
	spamThrottleRate      = 0.30
	spamPauseRate         = 0.50
	bounceWatchRate       = 0.03
	bounceThrottleRate    = 0.05
	bouncePauseRate       = 0.10
	complaintWatchRate    = 0.0003
	complaintThrottleRate = 0.001
	complaintPauseRate    = 0.003
)

// Minimum samples before a rate may influence anything. A rate below its minimum
// is UNPROVEN: it neither degrades a mailbox nor counts as clean evidence for
// promotion. Absence of evidence is never health.
const (
	MinPlacementSamples = 20
	MinBounceSamples    = 50
	// Complaint thresholds are fine-grained (0.03% / 0.1% / 0.3%), so they need a
	// far larger sample than the coarse spam bands to be resolvable at all. At 100
	// samples a SINGLE complaint yields a Wilson lower bound of ~0.18%, which clears
	// the 0.1% throttle threshold — so the old minimum let one FBL report throttle a
	// mailbox even with the bound applied. At 1000, one complaint bounds to ~0.018%
	// and reads as clean, while 20 bounds to ~1.3% and pauses.
	MinComplaintSamples = 1000
)

// ProbationDailyVolume caps probation and recovery lanes (design doc §5). Low
// enough to bound blast radius, high enough to accumulate placement evidence.
const ProbationDailyVolume = 5

// QuarantineCooldown is how long a quarantined participant must wait before it may
// enter recovery. Elapsing is NECESSARY BUT NOT SUFFICIENT — recovery still has to
// earn healthy through fresh evidence, which is acceptance criterion 2.
const QuarantineCooldown = 72 * time.Hour

// LaneEvidenceGrace is how long a healthy-lane participant may go without
// qualified evidence before it is returned to probation.
//
// It exists because losing SIGHT of a mailbox and finding something WRONG with it
// are different events that deserve different reflexes. Without a grace, one
// unqualified tick — a 7-day window sliding past its twentieth placement, a
// partner that did not poll, a snapshot refresh that skipped a workspace —
// demoted the mailbox on the next sweep and promoted it back on the one after:
// two audit rows, and a day capped at ProbationDailyVolume for a mailbox nothing
// was ever wrong with. Worse, the demotion is self-reinforcing: probation caps
// output, fewer sends produce fewer placements, and the sample needed to
// requalify takes days to rebuild. The cost of holding an unmeasured mailbox for
// a few hours is far below the cost of that ratchet.
//
// It applies ONLY to the absence of evidence. A degraded health state, an auth
// regression and a quarantine all still apply on the first sweep — containment
// must never wait, and none of them route through this branch.
//
// Six hours is ~72 sweeps at the five-minute cadence, so no transient can survive
// it, while a mailbox that has genuinely gone dark still leaves the healthy pool
// the same day.
const LaneEvidenceGrace = 6 * time.Hour

// Signals are the materialized evidence for one participant, read from
// warmup_signal_snapshots.
//
// Campaign and warmup bounce populations are deliberately SEPARATE. Phase 0 summed
// them, and warmup traffic — synthetic mail between the operator's own mailboxes,
// which essentially never hard-bounces — diluted the denominator below the
// thresholds it was meant to trip: 20 hard bounces on 200 campaign sends is a 10%
// rate, but 20/(200+1200) reads as 1.4%. Each population is evaluated against its
// own gate and the worst result wins.
type Signals struct {
	CurrentHealth string
	CurrentLane   string

	// AuthPassing is sending_domains.state = 'passing' for the mailbox's
	// organizational domain AND a mailbox that is connected. It deliberately does
	// not consider DKIM: selectors are not discoverable from DNS, so a missing
	// selector match is not evidence of an unsigned domain (see migration 000036).
	AuthPassing bool

	// EvidenceFresh reports whether the newest evidence ABOUT THIS MAILBOX is
	// recent enough to act on. Stale evidence is treated as no evidence, never as
	// health. It measures the age of the observations, not the age of the
	// snapshot row that aggregates them — a snapshot is rewritten every sweep and
	// so is always young, which made the earlier test vacuously true.
	EvidenceFresh bool

	// EvidenceLapsedSince is when this participant's health last FELL to unknown,
	// i.e. when it stopped having qualified evidence; zero when that has never
	// happened (or not since it was last proven). It anchors LaneEvidenceGrace and
	// nothing else — it can neither promote nor contain.
	EvidenceLapsedSince time.Time

	Inbox int
	Spam  int

	CampaignDelivered int
	// CampaignHardBounces is SELF-OBSERVED: Inroad's own DSN parser classified the
	// bounce. It carries full authority and can contain a mailbox.
	CampaignHardBounces int
	// CampaignAssertedHardBounces was posted by a holder of deliverability:write.
	// Real evidence, but the scope exists so an ingest credential cannot mutate
	// campaigns — so it may reduce volume and it may not contain. Same posture
	// invariant 39 gives the DNS advisory.
	CampaignAssertedHardBounces int
	CampaignComplaints          int

	WarmupDelivered   int
	WarmupHardBounces int

	// ObserverTokenFailures counts forged warmup tokens THIS mailbox received. It
	// is observer-side only and never attributed to a claimed sender, because an
	// unauthenticated token may name any sender — trusting it would let anyone
	// throttle a mailbox they do not own. Surfaced to operators; nothing automatic
	// acts on it until an observer-trust axis exists.
	ObserverTokenFailures int

	// QuarantinedSince is when the participant entered quarantine, zero if it is
	// not quarantined. Drives the cooldown gate only.
	QuarantinedSince time.Time

	// PausedUntil is the timed block from the last escalation. While it is in the
	// future a health RECOVERY is held back, which also blocks the lane promotion
	// that recovery would otherwise justify. Zero means no block.
	PausedUntil time.Time
}

// Decision is both the outcome and the evidence behind it. Every field that fed a
// threshold is recorded so warmup_state_transitions can explain the change without
// re-deriving anything.
type Decision struct {
	Health           string
	HealthReasonCode string
	HealthReason     string

	Lane           string
	LaneReasonCode string
	LaneReason     string

	PlacementSamples int
	SpamRate         float64

	CampaignBounceSamples int
	CampaignBounceRate    float64
	// AssertedBounceRate is the feed-reported arm, reported separately because it
	// is capped at watch and an operator needs to see which arm spoke.
	AssertedBounceRate  float64
	WarmupBounceSamples int
	WarmupBounceRate    float64

	ComplaintSamples int
	ComplaintRate    float64

	ObserverTokenFailures int
}

// DrivingBouncePair returns the bounce population that drove this decision, with
// ITS OWN denominator and rate — the triple warmup_state_transitions records.
//
// The table has one bounce column pair and two populations, so the writer has to
// choose. Returning all three values together is what keeps them consistent: three
// separate assignments at the call site could label the campaign denominator
// "warmup", and a wrong label in an append-only trail is worse than none.
//
// The higher rate is the driving one, because that is the arm a band was applied
// to. A tie — including no bounces at all in either arm — reports the campaign
// population: it is the one an operator is asking about, and equal rates are not a
// warmup finding.
func (d Decision) DrivingBouncePair() (population string, samples int, rate float64) {
	if d.WarmupBounceRate > d.CampaignBounceRate {
		return BouncePopulationWarmup, d.WarmupBounceSamples, d.WarmupBounceRate
	}
	return BouncePopulationCampaign, d.CampaignBounceSamples, d.CampaignBounceRate
}

// EvaluateParticipant decides both axes in one pass. The caller persists them in
// one statement, which is what makes "quarantined but healthy" unrepresentable.
//
// Health escalation drives lane demotion; lane never drives health.
func EvaluateParticipant(s Signals, now time.Time) Decision {
	d := evaluateHealth(s)
	// Applied here, BEFORE the lane is derived, so a health recovery the dwell is
	// holding back cannot leak into a lane promotion. Containment moves (auth
	// regressed, evidence stale, cooldown elapsed) are unaffected: they do not read
	// the recovered health, and delaying containment for a dwell timer would be
	// backwards. Idempotent, so a caller that also applies it changes nothing.
	d = HoldRecoveryDuringBlock(d, s.CurrentHealth, s.PausedUntil, now)
	d.Lane, d.LaneReasonCode, d.LaneReason = evaluateLane(s, d, now)
	return d
}

// evaluateHealth applies each rate's minimum-sample gate and Wilson lower bound,
// then takes the worst warranted state. Escalation is immediate; recovery is one
// step per evaluation.
func evaluateHealth(s Signals) Decision {
	placement := s.Inbox + s.Spam
	d := Decision{
		Health:           StateHealthy,
		PlacementSamples: placement,
		SpamRate:         qualifiedRate(s.Spam, placement, MinPlacementSamples),

		CampaignBounceSamples: s.CampaignDelivered,
		CampaignBounceRate:    qualifiedRate(s.CampaignHardBounces, s.CampaignDelivered, MinBounceSamples),
		AssertedBounceRate:    qualifiedRate(s.CampaignAssertedHardBounces, s.CampaignDelivered, MinBounceSamples),
		WarmupBounceSamples:   s.WarmupDelivered,
		WarmupBounceRate:      qualifiedRate(s.WarmupHardBounces, s.WarmupDelivered, MinBounceSamples),

		ComplaintSamples: s.CampaignDelivered,
		ComplaintRate:    qualifiedRate(s.CampaignComplaints, s.CampaignDelivered, MinComplaintSamples),

		ObserverTokenFailures: s.ObserverTokenFailures,
	}

	worst := func(state, code, reason string) {
		if stateRank(state) > stateRank(d.Health) {
			d.Health, d.HealthReasonCode, d.HealthReason = state, code, reason
		}
	}

	applyBand(d.SpamRate, spamWatchRate, spamThrottleRate, spamPauseRate, "spam", "spam placement rate", worst)
	applyBand(d.CampaignBounceRate, bounceWatchRate, bounceThrottleRate, bouncePauseRate, "campaign_bounce", "campaign hard-bounce rate", worst)
	// Asserted evidence is capped at watch, whatever the rate. Uncapped, roughly
	// seven forged POSTs reached StateThrottled -> degraded -> LaneQuarantine ->
	// the whole domain withheld for 72h, un-clearable by the tenant. Watch reduces
	// volume and surfaces the signal to an operator without letting an ingest
	// credential contain anything.
	if d.AssertedBounceRate > bounceWatchRate {
		worst(StateWatch, "campaign_bounce_asserted_watch",
			"reported campaign hard-bounce rate above the watch threshold (reported by the deliverability feed, so it reduces volume but does not contain)")
	}
	applyBand(d.WarmupBounceRate, bounceWatchRate, bounceThrottleRate, bouncePauseRate, "warmup_bounce", "warmup hard-bounce rate", worst)
	applyBand(d.ComplaintRate, complaintWatchRate, complaintThrottleRate, complaintPauseRate, "complaint", "complaint rate", worst)

	current := normalizeState(s.CurrentHealth)

	// No qualified placement evidence: hold a degraded state where it is, and call
	// an undegraded one unknown. Never healthy.
	if d.Health == StateHealthy && (placement < MinPlacementSamples || !s.EvidenceFresh) {
		if stateRank(current) > stateRank(StateHealthy) {
			d.Health, d.HealthReasonCode, d.HealthReason = current, "insufficient_evidence_to_recover", "not enough fresh placement evidence to recover"
			return d
		}
		d.Health, d.HealthReasonCode, d.HealthReason = StateUnknown, "placement_sample_insufficient", "not enough fresh placement evidence"
		return d
	}

	if stateRank(d.Health) < stateRank(current) {
		d.Health = stateAtRank(stateRank(current) - 1)
		d.HealthReasonCode, d.HealthReason = "recovery_step", "clean qualified window: recovering one state"
		return d
	}

	// The health axis ALWAYS names itself, even when it did not move. A transition
	// is written when EITHER axis changes, and both share one atomic statement
	// against a table that rejects an empty reason_code — so an unexplained health
	// decision does not merely lose its audit trail, it aborts the LANE decision
	// travelling with it, and the participant is stuck on every subsequent sweep.
	// (unknown shares healthy's rank, so the recovery-step branch above cannot fire
	// for unknown -> healthy; that promotion reaches here.)
	if d.HealthReasonCode == "" {
		if d.Health != current {
			d.HealthReasonCode, d.HealthReason = "evidence_qualified", "qualified placement evidence establishes health"
		} else {
			d.HealthReasonCode, d.HealthReason = "health_unchanged", "health is unchanged; this transition moves the pool lane"
		}
	}
	return d
}

// evaluateLane maps the health decision plus admission facts onto a pool lane.
//
// Automated policy never enters LaneBlocked. The design doc shows a severe signal
// reaching it directly, but blocked requires operator approval to LEAVE — so an
// automatic entry could strand a self-hosted install with no path out and no
// operator watching. Quarantine is the automated terminal containment; blocked
// stays operator-only in both directions.
func evaluateLane(s Signals, d Decision, now time.Time) (lane, code, reason string) {
	cur := normalizeLane(s.CurrentLane)

	// Operator-held states are never moved by policy.
	if cur == LaneBlocked {
		return LaneBlocked, "lane_blocked_held", "blocked: operator approval required to re-enter recovery"
	}

	// Quarantine is STRICTER than pending_auth — both seal the mailbox, but only
	// quarantine carries a cooldown and a requalification requirement. Letting an
	// auth regression move it would launder containment: the mailbox would return
	// through pending_auth -> probation, which may send and may take new leads.
	if cur == LaneQuarantine && !s.AuthPassing {
		return LaneQuarantine, "lane_quarantine_held", "quarantined; domain authentication is also failing"
	}

	// Admission prerequisite outranks everything else: unauthenticated mail damages
	// reputation faster than any pool arrangement can repair it.
	if !s.AuthPassing {
		if cur == LanePendingAuth {
			return LanePendingAuth, "lane_pending_auth", "domain authentication has not passed"
		}
		return LanePendingAuth, "lane_auth_regressed", "domain authentication no longer passes"
	}

	degraded := stateRank(d.Health) >= stateRank(StateThrottled)
	watching := d.Health == StateWatch
	qualified := d.Health == StateHealthy && s.EvidenceFresh &&
		d.PlacementSamples >= MinPlacementSamples && !promotionAlarmed(s)

	// Any qualified-throttled-or-worse signal contains the participant, wherever it
	// currently sits.
	if degraded {
		if cur == LaneQuarantine {
			return LaneQuarantine, "lane_quarantine_held", "quarantined: " + d.HealthReason
		}
		return LaneQuarantine, "lane_quarantined", "quarantined: " + d.HealthReason
	}

	switch cur {
	case LanePendingAuth:
		// Leaving pending_auth must not release an unexpired quarantine. A mailbox
		// can reach here FROM quarantine only via an auth regression, and the
		// transition trail still records when containment began.
		if !s.QuarantinedSince.IsZero() && now.Sub(s.QuarantinedSince) < QuarantineCooldown {
			return LaneQuarantine, "lane_quarantine_resumed", "domain authentication passed, but the quarantine cooldown has not elapsed"
		}
		// Authentication just passed. Entry is probation, never healthy — a
		// newly-authenticated mailbox has proven nothing about placement.
		return LaneProbation, "lane_admitted_to_probation", "domain authentication passed: entering probation"

	case LaneQuarantine:
		if s.QuarantinedSince.IsZero() || now.Sub(s.QuarantinedSince) < QuarantineCooldown {
			return LaneQuarantine, "lane_cooldown_active", "quarantine cooldown has not elapsed"
		}
		// Cooldown elapsed AND prerequisites met. Recovery still has to EARN
		// healthy: elapsed time alone never reinstates a participant.
		return LaneRecovery, "lane_cooldown_elapsed", "quarantine cooldown elapsed: entering recovery to requalify"

	case LaneProbation, LaneRecovery:
		if qualified {
			return LaneHealthy, "lane_qualified", "qualified clean window: promoted to the healthy pool"
		}
		return cur, "lane_awaiting_evidence", "awaiting a qualified clean window"

	case LaneWatch:
		if qualified {
			return LaneHealthy, "lane_recovered", "clean qualified evidence: returned to the healthy pool"
		}
		return LaneWatch, "lane_watch_held", "held on watch pending clean evidence"

	default: // LaneHealthy
		if watching {
			return LaneWatch, "lane_watch", "moved to watch: " + d.HealthReason
		}
		if !qualified {
			if holdsForEvidenceGrace(s, d, now) {
				return LaneHealthy, "lane_evidence_grace",
					"evidence lapsed recently: holding the healthy lane until the grace period elapses"
			}
			// Evidence went stale or fell below the minimum sample, and stayed that
			// way. Leaving it in the healthy lane would let an unmeasured mailbox keep
			// reaching healthy peers.
			return LaneProbation, "lane_evidence_lapsed", "no fresh qualified evidence: returned to probation"
		}
		return LaneHealthy, "lane_healthy", "qualified clean evidence"
	}
}

// holdsForEvidenceGrace reports whether a healthy-lane participant that failed to
// qualify should keep its lane for now. See LaneEvidenceGrace for why the grace
// exists.
//
// Three gates, each load-bearing:
//
//  1. Only StateUnknown qualifies for the hold. That state means exactly "no
//     qualified evidence" — an ABSENCE. Every other way to miss qualification is a
//     signal: watch and worse are handled by the branches above, and a healthy
//     state that fails promotionAlarmed is a small pile of BAD evidence, which is
//     information, not a blind spot, and still demotes immediately.
//
//  2. If the health axis is falling INTO unknown on this very evaluation, the
//     lapse starts now, so it is inside any grace. The transition this tick writes
//     (from a non-unknown state to unknown) is what EvidenceLapsedSince reads on
//     every subsequent sweep, so the marker exists from the second tick onward.
//
//  3. Health that is ALREADY unknown with no recorded fall has no lapse to date
//     from — a participant backfilled straight into the healthy lane, for
//     instance. It is not held: an unbounded hold on an unmeasured mailbox is the
//     failure mode the grace is supposed to bound, not create.
func holdsForEvidenceGrace(s Signals, d Decision, now time.Time) bool {
	// Alarming evidence is never "lack of evidence", so it is never graced —
	// even when it is too thin to ESCALATE. A thin sample cannot punish (that
	// needs the minimum, or false positives follow) but it must not buy a hold
	// either: 25 hard bounces on 49 recipients reaches evaluateHealth's
	// under-minimum branch, which rewrites health to unknown, and without this
	// gate that mailbox kept the healthy lane and full ramp volume for 6h.
	if promotionAlarmed(s) {
		return false
	}
	if d.Health != StateUnknown {
		return false
	}
	if normalizeState(s.CurrentHealth) != StateUnknown {
		return true
	}
	if s.EvidenceLapsedSince.IsZero() {
		return false
	}
	return now.Sub(s.EvidenceLapsedSince) < LaneEvidenceGrace
}

// LaneDailyVolume caps a lane's warmup output. Probation and recovery are
// deliberately low-volume: they exist to gather evidence with a bounded blast
// radius, not to make progress quickly. Lanes that may not send return 0.
func LaneDailyVolume(lane string, rampTarget int) int {
	switch normalizeLane(lane) {
	case LaneHealthy, LaneWatch:
		return rampTarget
	case LaneProbation, LaneRecovery:
		if rampTarget < ProbationDailyVolume {
			return rampTarget
		}
		return ProbationDailyVolume
	default: // pending_auth, quarantine, blocked
		return 0
	}
}

// LaneMaySend reports whether a lane may originate warmup traffic at all.
func LaneMaySend(lane string) bool {
	switch normalizeLane(lane) {
	case LaneHealthy, LaneWatch, LaneProbation, LaneRecovery:
		return true
	default:
		return false
	}
}

// LaneMayTakeNewLead reports whether a mailbox in this lane may be given a NEW
// campaign lead. Replies to a human who already wrote back are governed
// separately and are always allowed: stopping reputation expansion is the goal,
// and abandoning a live conversation is a business harm that outweighs it.
func LaneMayTakeNewLead(lane string) bool {
	switch normalizeLane(lane) {
	case LaneQuarantine, LaneBlocked, LanePendingAuth:
		return false
	default:
		return true
	}
}

// NewLeadsWithheld reports whether a mailbox must be refused NEW campaign leads,
// judging its own pool lane AND the worst lane on its organizational domain
// (design §7: the gate is mailbox AND domain). It is the ONE place that decision
// is made, so the preflight warning, the senders panel's cap_today and the
// rotation's eligibility cannot drift apart — they had three copies of an
// increasingly subtle expression between them.
//
// Domain scope exists because reputation is largely assessed per organizational
// domain: a quarantined mailbox on a domain has almost certainly damaged the
// standing of every other mailbox sending from it, so continuing to expand cold
// volume there spends a reputation that is already in trouble.
//
// Replies to a human who already wrote back are governed separately and are
// always allowed — see LaneMayTakeNewLead.
func NewLeadsWithheld(mailboxLane, domainLane string) bool {
	return laneWithholdsNewLeads(mailboxLane) || laneWithholdsNewLeads(domainLane)
}

// laneWithholdsNewLeads is NewLeadsWithheld for one lane value.
//
// Two lanes are deliberately exempt from the ones LaneMayTakeNewLead refuses:
//
//   - "" means the mailbox is not warming up at all (or its participant row is
//     disabled, whose stored lane is frozen history rather than a live signal).
//     Warmup is opt-in; not opting in cannot cost a mailbox its campaigns.
//   - pending_auth is derived from sending_domains, which security.md invariant 39
//     scopes as ADVISORY: "an advisory that turns out to be wrong must not be able
//     to stop a campaign". A spoofed or merely un-swept DNS answer would otherwise
//     block a launch, and a fresh install has no sending_domains row at all. It
//     still withholds warmup traffic, which is Inroad's own mail.
func laneWithholdsNewLeads(lane string) bool {
	if lane == "" || lane == LanePendingAuth {
		return false
	}
	return !LaneMayTakeNewLead(lane)
}

// LanesCompatible reports whether sender may send warmup to recipient. With no
// sentinel lane, same-lane is the whole rule — simple enough to be provable,
// which is the point: a healthy customer mailbox never receives traffic from a
// probation, recovery, watch, quarantined or blocked peer.
func LanesCompatible(sender, recipient string) bool {
	s, r := normalizeLane(sender), normalizeLane(recipient)
	return LaneMaySend(s) && LaneMaySend(r) && s == r
}

// promotionAlarmed reports whether any evidence arm looks bad enough to withhold
// promotion, INDEPENDENTLY of whether it met the minimum sample for escalation.
//
// The two questions are genuinely different. "Is there enough evidence to punish
// this mailbox?" needs a minimum sample, or thin samples produce false positives.
// "Is there enough evidence to vouch for it?" does not: a mailbox with 25 hard
// bounces on 49 recipients has not proven a rate, but it has certainly not earned
// the healthy pool. Absence of evidence is not health — and neither is a small pile
// of bad evidence.
func promotionAlarmed(s Signals) bool {
	return alarming(s.CampaignHardBounces, s.CampaignDelivered, bounceWatchRate) ||
		alarming(s.CampaignAssertedHardBounces, s.CampaignDelivered, bounceWatchRate) ||
		alarming(s.WarmupHardBounces, s.WarmupDelivered, bounceWatchRate) ||
		alarming(s.CampaignComplaints, s.CampaignDelivered, complaintWatchRate)
}

// alarming applies the Wilson lower bound with NO minimum-sample gate. A zero
// denominator is genuinely no evidence and never alarms.
func alarming(successes, trials int, watch float64) bool {
	if trials <= 0 || successes <= 0 {
		return false
	}
	if successes >= trials {
		return true
	}
	return wilsonLowerBound(successes, trials) > watch
}

// applyBand maps one rate onto the worst state its three thresholds warrant. A
// rate of exactly 0 never escalates, so an unproven rate (see qualifiedRate)
// cannot degrade anything.
func applyBand(rate, watch, throttle, pause float64, code, label string, worst func(state, code, reason string)) {
	switch {
	case rate > pause:
		worst(StatePaused, code+"_pause", label+" above the pause threshold")
	case rate > throttle:
		worst(StateThrottled, code+"_throttle", label+" above the throttle threshold")
	case rate > watch:
		worst(StateWatch, code+"_watch", label+" above the watch threshold")
	}
}

// qualifiedRate is the Wilson score interval's LOWER bound at 95% for
// successes/trials, or 0 when the sample has not met its minimum.
//
// Using the lower bound rather than the point estimate is what makes a small
// sample honest. Phase 0 compared 1 complaint in 100 sends (a point estimate of
// 1%) against a 0.3% pause threshold and paused the mailbox for 72 hours — but one
// FBL report is not evidence of a 0.3% rate. The lower bound for 1/100 is ~0.18%,
// which does not pause; 20/1000 gives ~1.3%, which does.
func qualifiedRate(successes, trials, minSamples int) float64 {
	if trials < minSamples || trials <= 0 || successes <= 0 {
		return 0
	}
	if successes >= trials {
		return 1
	}
	return wilsonLowerBound(successes, trials)
}

// wilsonLowerBound returns the lower bound of the Wilson score interval at 95%
// confidence (z = 1.96). Preferred over the normal approximation because it stays
// well-behaved at small n and near 0, which is exactly where reputation rates live.
func wilsonLowerBound(successes, trials int) float64 {
	const z = 1.959963984540054
	n := float64(trials)
	p := float64(successes) / n
	z2 := z * z
	center := p + z2/(2*n)
	margin := z * math.Sqrt(p*(1-p)/n+z2/(4*n*n))
	lb := (center - margin) / (1 + z2/n)
	if lb < 0 {
		return 0
	}
	return lb
}

// stateRank orders health states by badness. unknown deliberately shares
// healthy's rank: it is an ABSENCE of evidence, not a degree of badness, and
// ranking it as "worse" would make recovery arithmetic treat missing data as a
// penalty to be worked off.
func stateRank(state string) int {
	switch state {
	case StateWatch:
		return 1
	case StateThrottled:
		return 2
	case StatePaused:
		return 3
	default:
		return 0
	}
}

func stateAtRank(rank int) string {
	switch rank {
	case 1:
		return StateWatch
	case 2:
		return StateThrottled
	case 3:
		return StatePaused
	default:
		return StateHealthy
	}
}

func normalizeState(state string) string {
	if state == StateUnknown {
		return StateUnknown
	}
	return stateAtRank(stateRank(state))
}

// normalizeLane maps an unrecognized or empty lane to probation. A lane outside
// the CHECK constraint can only come from a direct write, and treating it as
// probation fails safe: bounded volume, own lane, no access to healthy peers.
func normalizeLane(lane string) string {
	switch lane {
	case LanePendingAuth, LaneProbation, LaneHealthy, LaneWatch, LaneRecovery, LaneQuarantine, LaneBlocked:
		return lane
	default:
		return LaneProbation
	}
}

// IsRecovery reports whether a health transition from->to moves toward a healthier
// state rather than a worse one. from == to is not a recovery.
func IsRecovery(from, to string) bool {
	return stateRank(to) < stateRank(from)
}

// HoldRecoveryDuringBlock keeps a health RECOVERY from landing while the timed
// block is still in force (pausedUntil in the future), enforcing the 24h/72h dwell
// so a mailbox cannot walk paused→throttled→watch→healthy on back-to-back
// five-minute sweeps.
//
// It clamps only the health axis. A lane change still applies immediately, because
// the reasons a lane must move — authentication regressed, cooldown elapsed,
// evidence went stale — are not recoveries being rushed, and delaying containment
// to respect a dwell timer would be backwards.
func HoldRecoveryDuringBlock(d Decision, fromHealth string, pausedUntil, now time.Time) Decision {
	if !IsRecovery(fromHealth, d.Health) || !pausedUntil.After(now) {
		return d
	}
	d.Health = normalizeState(fromHealth)
	if d.HealthReasonCode == "recovery_step" || d.HealthReasonCode == "evidence_qualified" {
		d.HealthReasonCode = "recovery_blocked_by_dwell"
		d.HealthReason = "recovery held: the timed block has not elapsed"
	}
	return d
}

// ShouldApplyTransition reports whether a decision differs from what is already
// stored on either axis. A no-op on both is never written, so a steady-state sweep
// touches nothing.
func ShouldApplyTransition(fromHealth, toHealth, fromLane, toLane string) bool {
	return fromHealth != toHealth || normalizeLane(fromLane) != normalizeLane(toLane)
}

// LeaseLifetime bounds how long an issued send lease stays claimable.
//
// It exists for the enqueue-to-pickup window, not for the microseconds between
// choosing a partner and claiming the send: a backed-up or retrying asynq queue can
// fire a warmup:send task long after the lane it was issued under was decided. Long
// enough that a healthy queue never trips it, short enough that a stale task dies
// rather than sends.
const LeaseLifetime = 15 * time.Minute

// Lease is the authority a send carries from the moment a partner is chosen to the
// moment the send is claimed. It is stored on the send row itself rather than in a
// separate table: that row already IS the reservation, and a second lifecycle to
// keep in step with it is the "two things that must agree, and don't" shape every
// repeated defect in this subsystem has taken.
type Lease struct {
	// IssuedLane is the sender's pool lane at issue time. EMPTY means the row
	// predates leases entirely.
	IssuedLane          string
	IssuedPolicyVersion string
	ExpiresAt           time.Time
}

// Lease refusal reason codes. A refusal is the system working, not an error, so it
// has to be legible: an operator must be able to tell "the queue is slow" from
// "this mailbox keeps getting quarantined mid-flight".
const (
	LeaseOK            = ""
	LeaseExpired       = "lease_expired"
	LeaseLaneChanged   = "lease_lane_changed"
	LeasePolicyChanged = "lease_policy_changed"
)

// IssueLease stamps a lease for a sender currently in currentLane. The single home
// for the expiry arithmetic, so no call site computes it and drifts.
func IssueLease(currentLane string, now time.Time) Lease {
	return Lease{
		IssuedLane:          normalizeLane(currentLane),
		IssuedPolicyVersion: PolicyVersion,
		ExpiresAt:           now.Add(LeaseLifetime),
	}
}

// LeaseValid reports whether a lease may still be claimed, and names why not when it
// cannot.
//
// The lane comparison is what satisfies acceptance criterion 7 — "a stale pair lease
// cannot send after quarantine". Expiry alone would not: a mailbox quarantined
// thirty seconds after issue is still inside its window, and checking the clock
// cannot see that. Comparing against CURRENT state can.
func LeaseValid(l Lease, currentLane string, now time.Time) (bool, string) {
	// Pre-lease rows first, before any other rule. A send written before this
	// mechanism existed has an empty lane AND a zero ExpiresAt, and evaluating
	// expiry first would read that zero as "expired at the epoch" and refuse every
	// historical row. Back-filling a fabricated lane instead would put a claim in
	// the record that was never true.
	if l.IssuedLane == "" {
		return true, LeaseOK
	}
	if !l.ExpiresAt.After(now) {
		return false, LeaseExpired
	}
	if l.IssuedLane != normalizeLane(currentLane) {
		return false, LeaseLaneChanged
	}
	// A threshold change takes effect immediately rather than after the in-flight
	// tail drains under the old policy.
	if l.IssuedPolicyVersion != PolicyVersion {
		return false, LeasePolicyChanged
	}
	return true, LeaseOK
}
