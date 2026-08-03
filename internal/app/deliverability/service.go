package deliverability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/deliverability"
)

// ErrInvalid is a caller error the endpoints map to 422: a threshold outside
// 0.1..100, an unknown event kind, or an ingest missing its email or idempotency
// key.
var ErrInvalid = errors.New("deliverability: invalid input")

// Wire values of the CampaignDeliverability.verdict enum.
const (
	verdictOk     = "ok"
	verdictWarn   = "warn"
	verdictPaused = "paused"
)

// campaignStatusRunning mirrors campaign.StatusRunning. Duplicated as a string
// because app packages do not import each other; the SQL guard inside
// PauseCampaignForBreach is the authority, this is only the early exit.
const campaignStatusRunning = "running"

// eventKindComplaint / eventKindBounce mirror the deliverability_events.kind
// CHECK constraint and the DeliverabilityEvent schema's enum.
const (
	eventKindComplaint = "complaint"
	eventKindBounce    = "bounce"
)

// Service holds the business rules: which window to judge a campaign on, what to
// do when the breaker fires, and what an ingested event is allowed to cause. The
// scoring itself is platform/deliverability's — this package never re-derives it.
type Service struct {
	store Store
	// now is injected so the rolling window is testable without sleeping. It is a
	// field rather than a package-level clock so nothing here holds global mutable
	// state.
	now func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// Assessment is one resolved judgement of a campaign: the evidence, the score
// computed from it, and the breaker's verdict on the SAME evidence. The score and
// the verdict cannot disagree because they are produced here, together, from one
// Inputs value (invariant 2).
type Assessment struct {
	Inputs     deliverability.Inputs
	Score      deliverability.Score
	Verdict    deliverability.Verdict
	Guardrails Guardrails
	// Status is the campaign's status at assessment time.
	Status string
	// Since is the window the evidence was actually gathered over — the rolling
	// window, or the wider fallback when the rolling one was too small a sample.
	Since time.Time
}

// BreakerOutcome is what an evaluation did. Paused is true only for the ONE
// evaluation that actually stopped the campaign; every later evaluation of the
// same already-paused campaign reports false.
type BreakerOutcome struct {
	Verdict deliverability.Verdict
	Paused  bool
}

// EvaluateBreaker is the circuit breaker: assess the campaign and, if a rate has
// breached its threshold on a large enough sample, stop the campaign and record
// why.
//
// It reads COMMITTED state only and is called AFTER a send is finalised, never
// inside the send transaction (invariant 5) — a bug in here must be incapable of
// failing a delivery.
//
// Three independent things must all hold before a campaign is stopped: the
// operator left auto-pause on, the verdict is pause (which already requires the
// minimum sample — invariant 1), and the campaign is still running. The last is
// checked here as an early exit and again inside the UPDATE, where it is the real
// exactly-once guarantee.
func (s *Service) EvaluateBreaker(ctx context.Context, ws, campaignID uuid.UUID) (BreakerOutcome, error) {
	a, err := s.assessCampaign(ctx, ws, campaignID)
	if err != nil {
		return BreakerOutcome{}, err
	}
	out := BreakerOutcome{Verdict: a.Verdict}
	if !a.Guardrails.AutoPauseEnabled ||
		a.Verdict.State != deliverability.VerdictPause ||
		a.Status != campaignStatusRunning {
		return out, nil
	}
	paused, err := s.store.PauseForBreach(ctx, ws, campaignID, PauseEvent{
		Reason: a.Verdict.Reason, Metric: a.Verdict.Metric,
		Value: a.Verdict.Value, Threshold: a.Verdict.Threshold,
		Delivered: a.Verdict.Delivered,
	})
	if err != nil {
		return out, fmt.Errorf("pause campaign %s for %s: %w", campaignID, a.Verdict.Reason, err)
	}
	out.Paused = paused
	return out, nil
}

// assessCampaign gathers the evidence and produces the score and verdict from it.
//
// The window is rolling, with a wider fallback when the rolling one holds too
// small a sample to judge. Rolling is the default because a campaign that bounced
// badly in week one and was then fixed must not stay stopped on its history, and
// one failing NOW must not be masked by a clean past. The fallback exists because
// a slow campaign can go a whole week without accumulating the minimum sample,
// and refusing to judge it at all would leave the safeguard permanently disarmed.
//
// BOTH windows are floored, by two separate bounds, and the rule behind both is
// the same one: evidence from before the operator could have acted on it is never
// grounds for an automatic stop.
//
//  1. guardrails_enabled_at — when this campaign came under supervision. Without
//     it the feature is safe to run and unsafe to MIGRATE: auto_pause_enabled
//     defaults TRUE, so the migration arms every existing campaign, and the first
//     tick afterwards would judge a slow one (under the minimum for the 7-day
//     window, so it falls back to a wider sample) on its entire lifetime — bounces
//     predating the feature, which nobody opted into and no dashboard ever showed.
//     Applies whatever the campaign's status, because a campaign that has never
//     been supervised has no supervised evidence to report either.
//
//  2. The last automatic pause, for a RUNNING campaign. This is what stops the
//     fallback from being a trap: without it, a campaign with a bad history that an
//     operator has restarted would be re-judged on the very evidence they just
//     overrode, pause again immediately, and loop — the fallback would have turned
//     "judge recent behaviour" into "judge all behaviour, forever". Evidence
//     already acted on is spent. It does NOT apply while the campaign is still
//     paused: there is no human decision to respect yet, and a report answering
//     "100 over 0 delivered" for a campaign the breaker just stopped would hide the
//     very evidence the operator opened the page to see.
//
// One window serves both the score and the verdict (invariant 2). Reporting an
// unfloored score next to a floored verdict was the tempting alternative — the
// dashboard would show pre-supervision bounces the breaker declines to act on —
// but the card derives its ok/warn from that same verdict, so the two halves of one
// response would then describe different periods. The floor is self-healing
// instead: it stops binding once it is older than the window, so it costs at most
// one week of narrower history, and Confidence plus the reported sample say so out
// loud for that week.
func (s *Service) assessCampaign(ctx context.Context, ws, campaignID uuid.UUID) (Assessment, error) {
	cfg, err := s.store.CampaignConfig(ctx, ws, campaignID)
	if err != nil {
		return Assessment{}, err
	}
	floor := cfg.EnabledAt
	if cfg.Status == campaignStatusRunning {
		lastPause, perr := s.store.LastPausedAt(ctx, ws, campaignID)
		if perr != nil {
			return Assessment{}, perr
		}
		floor = later(floor, lastPause)
	}
	now := s.now()
	rollingSince := later(now.AddDate(0, 0, -deliverability.WindowDays), floor)
	counts, err := s.store.CampaignCounts(ctx, ws, campaignID, rollingSince)
	if err != nil {
		return Assessment{}, err
	}
	since := rollingSince
	// The fallback reaches back only as far as the floor — never to the beginning of
	// time. That is the whole safeguard: the widest sample the breaker can ever act
	// on is "everything since this campaign came under supervision, or since the
	// operator last overrode a pause", whichever is later.
	if counts.Delivered < deliverability.MinDelivered && floor.Before(rollingSince) {
		wider, werr := s.store.CampaignCounts(ctx, ws, campaignID, floor)
		if werr != nil {
			return Assessment{}, werr
		}
		// Only take the wider sample if it IS wider. Equal counts mean the rolling
		// window already holds everything there is, and swapping in a window that
		// starts earlier would only make the reported period misleading.
		if wider.Delivered > counts.Delivered {
			counts, since = wider, floor
		}
	}
	mailboxes, err := s.store.CampaignSenderMailboxes(ctx, ws, campaignID)
	if err != nil {
		return Assessment{}, err
	}
	signals, err := s.store.Signals(ctx, ws, mailboxes, since)
	if err != nil {
		return Assessment{}, err
	}
	in := toInputs(counts, signals)
	return Assessment{
		Inputs: in,
		Score:  deliverability.Compute(in),
		Verdict: deliverability.Breach(in, deliverability.Thresholds{
			BouncePct:    cfg.BouncePausePct,
			ComplaintPct: cfg.ComplaintPausePct,
			MinDelivered: deliverability.MinDelivered,
		}),
		Guardrails: cfg.Guardrails,
		Status:     cfg.Status,
		Since:      since,
	}, nil
}

// CampaignReport is GET /campaigns/{id}/deliverability.
//
// It carries no per-day series. The frozen CampaignDeliverability schema has no
// such field, so emitting one would be payload the generated client cannot reach —
// the workspace rollup is where the series lives.
type CampaignReport struct {
	Score       deliverability.Score
	Guardrails  Guardrails
	PauseEvents []PauseEvent
	Verdict     string
}

// CampaignReport assesses the campaign and returns the SAME score the breaker
// would act on, plus its pause history. ErrNotFound for a campaign that is not
// this workspace's.
func (s *Service) CampaignReport(ctx context.Context, ws, campaignID uuid.UUID) (CampaignReport, error) {
	a, err := s.assessCampaign(ctx, ws, campaignID)
	if err != nil {
		return CampaignReport{}, err
	}
	events, err := s.store.PauseEvents(ctx, ws, campaignID)
	if err != nil {
		return CampaignReport{}, err
	}
	return CampaignReport{
		Score:       a.Score,
		Guardrails:  a.Guardrails,
		PauseEvents: events,
		Verdict:     verdict(a, len(events)),
	}, nil
}

// verdict maps an assessment onto the three wire values.
//
// `paused` means THE BREAKER HAS FIRED — a recorded pause event on a campaign
// that is currently stopped. A campaign whose rate is over its threshold but
// which is still running (auto-pause turned off) reports `warn`: it is trending
// bad and visibly so, and calling it paused when it is sending would be a lie the
// UI would repeat.
func verdict(a Assessment, pauseEvents int) string {
	if pauseEvents > 0 && a.Status != campaignStatusRunning {
		return verdictPaused
	}
	if a.Verdict.State == deliverability.VerdictOk {
		return verdictOk
	}
	return verdictWarn
}

// Report is GET /deliverability — the workspace rollup.
type Report struct {
	Score           deliverability.Score
	Series          []Point
	AtRiskMailboxes []Risk
	AtRiskDomains   []Risk
}

// Report scores the whole workspace over the plain rolling window. No pause-event
// bound applies: a workspace is not a thing the breaker stops, so there is no
// human decision to avoid overriding.
func (s *Service) Report(ctx context.Context, ws uuid.UUID) (Report, error) {
	since := s.now().AddDate(0, 0, -deliverability.WindowDays)
	counts, err := s.store.WorkspaceCounts(ctx, ws, since)
	if err != nil {
		return Report{}, err
	}
	signals, err := s.store.Signals(ctx, ws, nil, since)
	if err != nil {
		return Report{}, err
	}
	score := deliverability.Compute(toInputs(counts, signals))
	series, err := s.store.Series(ctx, ws, since)
	if err != nil {
		return Report{}, err
	}
	mailboxes, err := s.store.AtRiskMailboxes(ctx, ws)
	if err != nil {
		return Report{}, err
	}
	domains, err := s.store.AtRiskDomains(ctx, ws)
	if err != nil {
		return Report{}, err
	}
	return Report{
		Score: score, Series: maskUnmeasured(series, score),
		AtRiskMailboxes: mailboxes, AtRiskDomains: domains,
	}, nil
}

// Guardrails reads one campaign's breaker settings.
func (s *Service) Guardrails(ctx context.Context, ws, campaignID uuid.UUID) (Guardrails, error) {
	cfg, err := s.store.CampaignConfig(ctx, ws, campaignID)
	if err != nil {
		return Guardrails{}, err
	}
	return cfg.Guardrails, nil
}

// SetGuardrails validates and persists the breaker settings. A threshold outside
// 0.1..100 is ErrInvalid: the floor exists because 0 means "pause at any rate at
// all", which every campaign past the sample floor would trip.
func (s *Service) SetGuardrails(ctx context.Context, ws, campaignID uuid.UUID, g Guardrails) (Guardrails, error) {
	if err := validThreshold("bounce_pause_pct", g.BouncePausePct); err != nil {
		return Guardrails{}, err
	}
	if err := validThreshold("complaint_pause_pct", g.ComplaintPausePct); err != nil {
		return Guardrails{}, err
	}
	if err := s.store.SetGuardrails(ctx, ws, campaignID, g); err != nil {
		return Guardrails{}, err
	}
	return g, nil
}

func validThreshold(field string, pct float64) error {
	if pct < deliverability.ThresholdMin || pct > deliverability.ThresholdMax {
		return fmt.Errorf("%w: %s must be between %v and %v",
			ErrInvalid, field, deliverability.ThresholdMin, deliverability.ThresholdMax)
	}
	return nil
}

// IngestResult tells the handler which status to return: 202 for a newly accepted
// event, 200 for a duplicate replay that changed nothing.
type IngestResult struct {
	Duplicate bool
}

// Ingest records one externally reported deliverability event.
//
// Idempotent on provider_event_id: a webhook that redelivers must not inflate the
// rate the breaker reads, so a duplicate writes nothing AND causes nothing —
// no second suppression, no second evaluation.
//
// A complaint additionally suppresses the address workspace-wide. That write runs
// BEFORE the evaluation and its error is returned, because it is the compliance
// half of the operation: a complaint is the strongest opt-out signal there is, and
// a failure to score must never be able to skip honouring it. Same ordering, and
// the same idempotent suppression table, as the reply-unsubscribe path.
//
// An ingested BOUNCE does not suppress. Provider bounce notifications include soft
// bounces (a full mailbox, a greylisting deferral), and suppressing an address
// forever on a temporary failure is not recoverable by the operator. Hard bounces
// are already suppressed where they are actually classified — the inbox poller —
// so an ingested bounce counts toward the rate and nothing more.
func (s *Service) Ingest(ctx context.Context, ws uuid.UUID, in EventInput) (IngestResult, error) {
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	in.Email = strings.TrimSpace(in.Email)
	in.ProviderEventID = strings.TrimSpace(in.ProviderEventID)
	if in.Kind != eventKindComplaint && in.Kind != eventKindBounce {
		return IngestResult{}, fmt.Errorf("%w: kind must be %q or %q", ErrInvalid, eventKindComplaint, eventKindBounce)
	}
	if in.Email == "" {
		return IngestResult{}, fmt.Errorf("%w: email is required", ErrInvalid)
	}
	if in.ProviderEventID == "" {
		return IngestResult{}, fmt.Errorf("%w: provider_event_id is required", ErrInvalid)
	}

	recorded, err := s.store.Ingest(ctx, ws, in)
	if err != nil {
		return IngestResult{}, err
	}
	if !recorded {
		return IngestResult{Duplicate: true}, nil
	}
	if in.Kind == eventKindComplaint {
		if err := s.store.Suppress(ctx, ws, in.Email, suppressionReasonComplaint); err != nil {
			return IngestResult{}, fmt.Errorf("suppress complained address: %w", err)
		}
	}
	s.evaluateAfterIngest(ctx, ws, in)
	return IngestResult{}, nil
}

// evaluateAfterIngest re-runs the breaker for the campaign this event belongs to.
// A complaint spike can happen with no new sends, so ingest has to trigger an
// evaluation of its own rather than waiting for the next finalised send.
//
// It is best-effort and never fails the request. The event is already committed
// and idempotent, so returning an error would only make the caller retry a
// delivery that would then be a duplicate — and the next finalised send in that
// campaign evaluates again anyway. Failures are logged, not swallowed silently.
//
// An event with no send_id, or one whose send has since been deleted, attributes
// to no campaign; there is nothing to evaluate.
func (s *Service) evaluateAfterIngest(ctx context.Context, ws uuid.UUID, in EventInput) {
	if in.SendID == nil {
		return
	}
	campaignID, err := s.store.SendCampaign(ctx, ws, *in.SendID)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			slog.ErrorContext(ctx, "deliverability_ingest_send_lookup_failed", "err", err)
		}
		return
	}
	out, err := s.EvaluateBreaker(ctx, ws, campaignID)
	if err != nil {
		slog.ErrorContext(ctx, "deliverability_ingest_evaluate_failed",
			"campaign_id", campaignID, "err", err)
		return
	}
	if out.Paused {
		slog.WarnContext(ctx, "campaign_auto_paused",
			"campaign_id", campaignID, "reason", out.Verdict.Reason,
			"value", out.Verdict.Value, "threshold", out.Verdict.Threshold,
			"delivered", out.Verdict.Delivered, "trigger", "ingest")
	}
}

// toInputs projects the stored evidence onto the pure package's Inputs.
//
// The one non-obvious mapping is Complained: it is a pointer that stays NIL until
// a complaint feed has actually reported for this workspace. Passing 0 instead
// would tell an operator their complaint rate is clean when nobody ever measured
// it (invariant 4).
//
// Placement is always passed as pointers-to-counts, because "observed nothing" is
// already expressible as inbox+spam == 0 and the pure package treats that as
// unmeasured — one rule for absence, in one place.
func toInputs(c Counts, sig Signals) deliverability.Inputs {
	in := deliverability.Inputs{
		Delivered:   c.Delivered,
		Bounced:     c.Bounced,
		SpamPlaced:  &sig.SpamPlaced,
		InboxPlaced: &sig.InboxPlaced,
		WarmupState: sig.WarmupState,
		DomainState: sig.DomainState,
	}
	if c.ComplaintFeed {
		in.Complained = &c.Complained
	}
	return in
}

// later returns whichever instant is later, treating a zero time as "no bound".
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// maskUnmeasured drops the per-day counts whose SCORE component was not measured.
// The series and the score describe the same window, so a chart plotting zero
// complaints a day under a score that declines to claim a complaint rate would
// contradict it — and the zero is the more convincing of the two. Whether a signal
// was measured is decided once, by the score, and the series follows it.
func maskUnmeasured(points []Point, score deliverability.Score) []Point {
	complaints, placement := false, false
	for _, c := range score.Components {
		switch c.Key {
		case deliverability.KeyComplaint:
			complaints = c.Measured
		case deliverability.KeySpamPlacement:
			placement = c.Measured
		}
	}
	out := make([]Point, len(points))
	for i, p := range points {
		out[i] = p
		out[i].ComplaintMeasured = complaints
		out[i].PlacementMeasured = placement
	}
	return out
}
