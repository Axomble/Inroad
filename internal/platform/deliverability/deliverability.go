// Package deliverability is the arithmetic of "how healthy is this campaign's
// sending, and should it be stopped": the health score an operator reads and the
// circuit-breaker verdict the worker acts on. Pure — no database, no clock, no
// config — so the breaker's decision and the dashboard's number come from ONE
// computation over the SAME inputs (invariant 2). A dashboard that scores a
// campaign differently from the breaker that pauses it is worse than either
// alone, and a second implementation of this math is exactly how that happens.
//
// Style follows internal/platform/sendcap and internal/platform/cadence: every
// threshold is a named constant carrying the reason it holds that value, because
// a bare 8.0 in a breaker is a number nobody can safely change later.
package deliverability

import "math"

// Warmup health states, mirroring the warmup_participants.health_state CHECK
// constraint. Duplicated as plain strings rather than imported: platform
// packages do not depend on each other (sendcap does the same), and this one
// must stay free of anything but its own arithmetic.
const (
	WarmupUnknown   = "unknown"
	WarmupWatch     = "watch"
	WarmupThrottled = "throttled"
	WarmupPaused    = "paused"
)

// Sending-domain authentication states, mirroring the sending_domains.state
// CHECK constraint. Only DomainFailing carries a penalty: "unknown" means a
// resolver could not answer, which is not evidence about anyone's DNS.
const (
	DomainPassing = "passing"
	DomainFailing = "failing"
	DomainUnknown = "unknown"
)

// Penalty ceilings and the rates at which each one saturates. The shape matches
// the published Google/Yahoo bulk-sender tolerances rather than being tuned to
// look nice: those are the numbers that decide whether mail lands.
const (
	// BouncePenalty is the most a bounce rate can cost. Bounces are the loudest
	// list-quality signal a receiving domain reads, and the one Inroad measures
	// most reliably, so it shares the largest ceiling with spam placement.
	BouncePenalty = 40
	// BounceSaturationPct is where the bounce penalty reaches BouncePenalty. 10%
	// is roughly double the "your list is bad" line the bulk-sender guidance
	// draws; past it there is nothing left to say, so the score stops moving and
	// the pause threshold (well below it) is what acts.
	BounceSaturationPct = 10.0

	// ComplaintPenalty is the most a complaint rate can cost. Lower ceiling than
	// bounce not because complaints matter less — they matter more per event —
	// but because they saturate 30x sooner, so the penalty is steep long before
	// the rate looks large.
	ComplaintPenalty = 30
	// ComplaintSaturationPct is where the complaint penalty reaches
	// ComplaintPenalty. Gmail/Yahoo publish 0.30% as the rate at which a sender
	// is in real trouble ("stay under 0.10%, never exceed 0.30%"), so that is
	// the point past which the score has nothing further to add.
	ComplaintSaturationPct = 0.30

	// SpamPlacementPenalty is the most spam-vs-inbox placement can cost. Same
	// ceiling as bounce: placement is a DIRECT observation of where our mail
	// lands, which is the thing every other signal is a proxy for.
	SpamPlacementPenalty = 40
	// SpamSaturationPct is where the placement penalty reaches
	// SpamPlacementPenalty. 40% of warmup mail in spam is a mailbox whose
	// outbound reputation has already collapsed; being worse than that changes
	// no decision.
	SpamSaturationPct = 40.0

	// Warmup-state penalties. The warmup engine has already judged this
	// mailbox's trailing placement, so the score inherits its verdict rather
	// than re-deriving one: a throttled mailbox must read as degraded even when
	// its bounce numbers are spotless.
	WarmupWatchPenalty     = 10
	WarmupThrottledPenalty = 25
	WarmupPausedPenalty    = 50

	// DomainAuthPenalty is what a `failing` SPF/DMARC verdict costs. Deliberately
	// below BouncePenalty even though unauthenticated mail is the single most
	// common cause of spam-foldering: the verdict is advisory (nothing on the
	// send path reads sending_domains, and DKIM selectors are not discoverable
	// from DNS), so it must not be able to dominate a score built from measured
	// outcomes.
	DomainAuthPenalty = 20
)

// Circuit-breaker defaults. Each is the value campaigns.* defaults to in
// migration 000037; a campaign may raise or lower its own two percentages.
const (
	// DefaultBouncePausePct is the bounce rate at which a campaign stops itself.
	// 8% sits above the ~5% that means "this list needs attention" and below the
	// 10%+ at which receiving domains start rejecting outright — high enough
	// that a merely mediocre list is not stopped for an operator who would
	// rather be told, low enough to stop a genuinely dead list before it burns
	// the sending domain.
	DefaultBouncePausePct = 8.0
	// DefaultComplaintPausePct is the complaint rate at which a campaign stops
	// itself. 1.5% is 5x the 0.30% "never exceed" line: a complaint rate is a
	// far more damaging signal than a bounce rate, but it is also measured from
	// a much sparser feed, so the pause threshold is set where the evidence is
	// unambiguous rather than where the harm begins (the SCORE is what reports
	// the harm beginning, from 0.30%).
	DefaultComplaintPausePct = 1.5

	// MinDelivered is the sample below which the breaker CANNOT fire, whatever
	// the ratio (invariant 1). This is the single reason the feature is safe to
	// enable by default: one bounce in the first three sends is a 33% bounce
	// rate, so without a floor every campaign would pause itself on launch and
	// the safeguard would be worse than not having it.
	MinDelivered = 50

	// WindowDays is the rolling window the rates are measured over. A campaign
	// that bounced badly in week one and was then fixed must not stay paused on
	// its history, and one failing NOW must not be masked by a clean past.
	WindowDays = 7

	// WarnFraction is where the early-warning band starts, as a fraction of each
	// pause threshold. An operator hears "this is trending bad" at half the
	// threshold, before they hear "this is stopped".
	WarnFraction = 0.5

	// HighConfidenceDelivered is the sample at which a score is trustworthy on
	// volume alone. 500 delivered puts a 1% rate at 5 events — enough that one
	// more or fewer does not move the verdict.
	HighConfidenceDelivered = 500
)

// ThresholdMin and ThresholdMax bound a caller-supplied percentage, matching the
// CampaignGuardrails schema and the campaigns_* CHECK constraints. The floor is
// 0.1 rather than 0 because a threshold of 0 means "pause at any rate at all",
// which every campaign past MinDelivered would trip instantly.
const (
	ThresholdMin = 0.1
	ThresholdMax = 100.0
)

// Component keys. They are the wire values of ScoreComponent.key, so they are
// part of the frozen API contract, not internal labels.
const (
	KeyBounce        = "bounce"
	KeyComplaint     = "complaint"
	KeySpamPlacement = "spam_placement"
	KeyWarmup        = "warmup"
	KeyDomainAuth    = "domain_auth"
)

// Verdict reasons and metrics, the wire values of CampaignPauseEvent.reason and
// .metric (and of the campaign verdict's warn cause).
const (
	ReasonBounceSpike    = "bounce_spike"
	ReasonComplaintSpike = "complaint_spike"
	MetricBounceRate     = "bounce_rate"
	MetricComplaintRate  = "complaint_rate"
)

// Inputs is the measured evidence for one campaign or workspace over a window.
//
// A nil pointer means NOT MEASURED and is excluded from the score entirely
// (invariant 4); a zero value means measured-and-clean. The distinction is
// load-bearing: Inroad has no complaint feed by default, and scoring a nil
// complaint count as 0 would tell an operator their complaint rate is fine when
// nobody ever looked.
type Inputs struct {
	// Delivered is the sample: sends that actually went out in the window.
	Delivered int
	// Bounced is always measured — Inroad detects hard bounces itself — so it is
	// a plain int. Zero means zero.
	Bounced int
	// Complained is nil until a complaint feed has reported at least once for
	// this workspace. See invariant 4.
	Complained *int
	// SpamPlaced / InboxPlaced come from warmup receipts, attributed to the
	// SENDER. Both nil when no placement has been observed at all: a mailbox
	// that is not warming up tells us nothing about where its mail lands.
	SpamPlaced  *int
	InboxPlaced *int
	// WarmupState is the worst warmup health among the relevant mailboxes:
	// healthy|watch|throttled|paused, or "" for mailboxes that are not warming
	// up (not a health signal, so not a penalty).
	WarmupState string
	// DomainState is the worst sending-domain verdict: passing|failing|unknown,
	// or "" when no domain has been checked. Only `failing` is penalised.
	DomainState string
}

// Thresholds is one campaign's circuit-breaker configuration.
type Thresholds struct {
	BouncePct    float64
	ComplaintPct float64
	// MinDelivered is the invariant-1 floor. Zero or negative falls back to the
	// MinDelivered constant — a caller that forgets it gets the safe value, not
	// a breaker with no floor.
	MinDelivered int
}

// DefaultThresholds is the on-by-default configuration a campaign starts with.
func DefaultThresholds() Thresholds {
	return Thresholds{
		BouncePct:    DefaultBouncePausePct,
		ComplaintPct: DefaultComplaintPausePct,
		MinDelivered: MinDelivered,
	}
}

// normalized replaces non-positive values with their defaults. A threshold of 0
// would make `rate >= threshold` true for every campaign past the sample floor,
// so a corrupted or unset value must fail toward "do not pause", never toward
// "pause everything".
func (t Thresholds) normalized() Thresholds {
	if t.BouncePct <= 0 {
		t.BouncePct = DefaultBouncePausePct
	}
	if t.ComplaintPct <= 0 {
		t.ComplaintPct = DefaultComplaintPausePct
	}
	if t.MinDelivered <= 0 {
		t.MinDelivered = MinDelivered
	}
	return t
}

// Confidence is how far the score should be trusted. A high score at low
// confidence is not a clean bill of health, which is why the UI renders it
// alongside the number rather than instead of it.
type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Component is one signal's contribution to the score. Measured=false means the
// signal is ABSENT — the UI renders "not measured", never 0% — and Rate is nil
// in exactly that case.
type Component struct {
	Key      string
	Label    string
	Penalty  int
	Rate     *float64
	Measured bool
	Detail   string
}

// Score is the computed health of one campaign or workspace.
type Score struct {
	Value      int
	Delivered  int
	Components []Component
	Confidence Confidence
}

// VerdictState is what the breaker concluded.
type VerdictState string

const (
	// VerdictOk: nothing to act on, including every case below the sample floor.
	VerdictOk VerdictState = "ok"
	// VerdictWarn: a rate is in the band below its pause threshold. Visible,
	// never acted on.
	VerdictWarn VerdictState = "warn"
	// VerdictPause: a rate has reached its threshold on a large enough sample.
	// The campaign should be stopped.
	VerdictPause VerdictState = "pause"
)

// Verdict is the breaker's decision plus everything needed to explain it, so an
// auto-paused campaign is never found stopped with no reason (invariant 3).
// Reason/Metric are empty on VerdictOk.
type Verdict struct {
	State     VerdictState
	Reason    string
	Metric    string
	Value     float64
	Threshold float64
	Delivered int
}

// Rate is part as a PERCENTAGE of whole, guarded: 0 when whole is 0 or negative,
// because "no sample" is not "a 0% rate" and dividing by it is not a thing the
// caller should have to remember. Percent (not a 0..1 fraction) because every
// threshold, constant and wire field in this feature is a percentage; one unit
// throughout is what stops an 8 from ever being compared against a 0.08.
func Rate(part, whole int) float64 {
	if whole <= 0 || part <= 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

// Compute is the score. It starts at 100 and subtracts each measured signal's
// penalty, flooring at 0; an unmeasured signal contributes NO penalty and is
// reported with Measured=false so the caller can say so out loud.
//
// Every input scores through this one function — the breaker's verdict comes
// from Breach over the same Inputs — so the dashboard and the safeguard cannot
// disagree (invariant 2).
func Compute(in Inputs) Score {
	comps := components(in)
	value := 100
	for _, c := range comps {
		value -= c.Penalty
	}
	if value < 0 {
		value = 0
	}
	return Score{
		Value:      value,
		Delivered:  in.Delivered,
		Components: comps,
		Confidence: confidence(in, comps),
	}
}

// components is the full signal set, in the order the dashboard renders them:
// the two rates an operator acts on first, then the observations behind them.
func components(in Inputs) []Component {
	return []Component{
		bounceComponent(in),
		complaintComponent(in),
		spamPlacementComponent(in),
		warmupComponent(in),
		domainComponent(in),
	}
}

// bounceComponent is measured whenever there is a sample to measure it over.
// With no delivered mail there is no bounce rate — not a 0% one.
func bounceComponent(in Inputs) Component {
	c := Component{Key: KeyBounce, Label: "Bounce rate"}
	if in.Delivered <= 0 {
		c.Detail = "no delivered mail in the window"
		return c
	}
	rate := Rate(in.Bounced, in.Delivered)
	c.Measured, c.Rate = true, &rate
	c.Penalty = saturatingPenalty(rate, BounceSaturationPct, BouncePenalty)
	return c
}

// complaintComponent is the invariant-4 case: nil Complained means no feed has
// ever reported, so there is no rate to show and no penalty to apply.
func complaintComponent(in Inputs) Component {
	c := Component{Key: KeyComplaint, Label: "Complaint rate"}
	if in.Complained == nil {
		c.Detail = "no complaint feed connected"
		return c
	}
	if in.Delivered <= 0 {
		c.Detail = "no delivered mail in the window"
		return c
	}
	rate := Rate(*in.Complained, in.Delivered)
	c.Measured, c.Rate = true, &rate
	c.Penalty = saturatingPenalty(rate, ComplaintSaturationPct, ComplaintPenalty)
	return c
}

// spamPlacementComponent reads the warmup pool's sender-attributed placement.
// Measured only when something was actually observed: nil counts, or an observed
// total of zero, both mean nobody has seen where this mail lands.
func spamPlacementComponent(in Inputs) Component {
	c := Component{Key: KeySpamPlacement, Label: "Spam placement"}
	if in.SpamPlaced == nil || in.InboxPlaced == nil {
		c.Detail = "no warmup placement observed"
		return c
	}
	observed := *in.SpamPlaced + *in.InboxPlaced
	if observed <= 0 {
		c.Detail = "no warmup placement observed"
		return c
	}
	rate := Rate(*in.SpamPlaced, observed)
	c.Measured, c.Rate = true, &rate
	c.Penalty = saturatingPenalty(rate, SpamSaturationPct, SpamPlacementPenalty)
	return c
}

// warmupComponent inherits the warmup engine's own verdict. An empty state is a
// mailbox that is not warming up, which is not a health signal; an unrecognized
// state can only come from a direct write, and silently penalising a healthy
// mailbox on a typo would be worse than ignoring it (same rule as
// sendcap.ColdFactor).
func warmupComponent(in Inputs) Component {
	c := Component{Key: KeyWarmup, Label: "Warmup health"}
	switch in.WarmupState {
	case WarmupUnknown:
		c.Detail = "insufficient warmup evidence"
		return c
	case WarmupWatch:
		c.Penalty = WarmupWatchPenalty
	case WarmupThrottled:
		c.Penalty = WarmupThrottledPenalty
	case WarmupPaused:
		c.Penalty = WarmupPausedPenalty
	case "":
		c.Detail = "not warming up"
		return c
	}
	c.Measured = true
	c.Detail = in.WarmupState
	return c
}

// domainComponent penalises only a `failing` verdict. `unknown` is a lookup that
// did not complete, which is not evidence, and an empty state is a domain never
// checked — neither is a measured signal.
func domainComponent(in Inputs) Component {
	c := Component{Key: KeyDomainAuth, Label: "Domain authentication"}
	switch in.DomainState {
	case DomainFailing:
		c.Penalty, c.Measured, c.Detail = DomainAuthPenalty, true, DomainFailing
	case DomainPassing:
		c.Measured, c.Detail = true, DomainPassing
	default:
		c.Detail = "not checked"
	}
	return c
}

// saturatingPenalty scales linearly from 0 at rate 0 to ceiling at saturation,
// then stops. Rounded rather than truncated so a rate just under a whole point
// does not silently cost nothing.
func saturatingPenalty(rate, saturation float64, ceiling int) int {
	if rate <= 0 || saturation <= 0 {
		return 0
	}
	if rate >= saturation {
		return ceiling
	}
	return int(math.Round(rate / saturation * float64(ceiling)))
}

// confidence is driven by BOTH halves of "how much do we know": the sample the
// rates were computed over, and how many of the five signals were measured at
// all. A perfect score over 12 delivered with no complaint feed and no placement
// data is a guess, and must not read as a clean bill of health.
//
// It reads the COMPONENTS' Measured flags rather than the raw Inputs pointers,
// because "measured" is a property of the evidence, not of how the caller chose
// to pass it: a store that hands over a pointer to a zero placement count has
// still observed nothing, and must not buy confidence with it.
func confidence(in Inputs, comps []Component) Confidence {
	measured := 0
	byKey := make(map[string]bool, len(comps))
	for _, c := range comps {
		byKey[c.Key] = c.Measured
		if c.Measured {
			measured++
		}
	}
	switch {
	case in.Delivered < MinDelivered || measured < 2:
		return ConfidenceLow
	case in.Delivered >= HighConfidenceDelivered && byKey[KeyComplaint] && byKey[KeySpamPlacement]:
		return ConfidenceHigh
	default:
		return ConfidenceMedium
	}
}

// Breach is the circuit-breaker decision over the same Inputs the score is built
// from.
//
// The FIRST thing it does is enforce invariant 1: below the minimum delivered
// count it returns VerdictOk whatever the ratio, so a campaign that bounced its
// first three sends is never stopped on a 33% "rate". That guard is what makes
// auto-pause safe to ship on by default.
//
// Bounce is evaluated before complaint because bounce is the signal Inroad
// measures itself and can therefore always act on; a complaint spike on a feed
// that has reported twice is the weaker evidence of the two. Only one reason is
// reported, and pausing always wins over warning.
func Breach(in Inputs, t Thresholds) Verdict {
	t = t.normalized()
	if in.Delivered < t.MinDelivered {
		return Verdict{State: VerdictOk, Delivered: in.Delivered}
	}
	bounce := Rate(in.Bounced, in.Delivered)
	var complaint float64
	complaintMeasured := in.Complained != nil
	if complaintMeasured {
		complaint = Rate(*in.Complained, in.Delivered)
	}

	switch {
	case bounce >= t.BouncePct:
		return breached(VerdictPause, ReasonBounceSpike, MetricBounceRate, bounce, t.BouncePct, in.Delivered)
	case complaintMeasured && complaint >= t.ComplaintPct:
		return breached(VerdictPause, ReasonComplaintSpike, MetricComplaintRate, complaint, t.ComplaintPct, in.Delivered)
	case bounce >= t.BouncePct*WarnFraction:
		return breached(VerdictWarn, ReasonBounceSpike, MetricBounceRate, bounce, t.BouncePct, in.Delivered)
	case complaintMeasured && complaint >= t.ComplaintPct*WarnFraction:
		return breached(VerdictWarn, ReasonComplaintSpike, MetricComplaintRate, complaint, t.ComplaintPct, in.Delivered)
	default:
		return Verdict{State: VerdictOk, Delivered: in.Delivered}
	}
}

// breached assembles a non-Ok verdict. Threshold is always the PAUSE threshold,
// including on a warn: "9.2% against a threshold of 8%" and "5% against a
// threshold of 8%" are the two sentences an operator needs, and reporting half
// the threshold on a warn would invent a limit that does not exist.
func breached(state VerdictState, reason, metric string, value, threshold float64, delivered int) Verdict {
	return Verdict{
		State: state, Reason: reason, Metric: metric,
		Value: value, Threshold: threshold, Delivered: delivered,
	}
}
