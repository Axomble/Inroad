package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/deliverability"
)

// HealthRisk is one at-risk mailbox or sending domain: what it is and why it
// is flagged. A count with no reason is not something a model can act on.
type HealthRisk struct{ Label, Reason string }

// WorkspaceHealth is the deliverability rollup for the whole workspace. The
// daily series the dashboard renders is deliberately not carried here — a
// model reasons from the score and its components, not from 30 rows of counts.
type WorkspaceHealth struct {
	Score           deliverability.Score
	AtRiskMailboxes []HealthRisk
	AtRiskDomains   []HealthRisk
}

// HealthPauseEvent is one recorded automatic pause of a campaign.
type HealthPauseEvent struct {
	Reason    string
	Metric    string
	Value     float64
	Threshold float64
	Delivered int
	CreatedAt time.Time
}

// CampaignHealth is one campaign's score, the circuit-breaker settings it is
// judged against, and the pauses the breaker has already caused.
type CampaignHealth struct {
	Score             deliverability.Score
	Verdict           string
	AutoPauseEnabled  bool
	BouncePausePct    float64
	ComplaintPausePct float64
	PauseEvents       []HealthPauseEvent
}

// Snapshot is the workspace pulse: the O(1) "what is happening right now"
// payload the console header reads, in the shape a model can summarise.
type Snapshot struct {
	MailboxesTotal, MailboxesActive, MailboxesPaused, MailboxesError    int64
	WarmupPool, WarmupUnknown, WarmupHealthy, WarmupWatch, WarmupAtRisk int64
	CampaignsTotal, CampaignsRunning, CampaignsDraft, CampaignsPaused   int64
	ContactsTotal                                                       int64
	SentToday, DailyCap                                                 int64
	Attention                                                           []SnapshotAttention
}

// SnapshotAttention is one server-defined "needs attention" row, carried
// through verbatim so a new backend signal reaches the agent with no tool
// change.
type SnapshotAttention struct {
	Kind     string
	Severity string
	Count    int64
	Reason   string
}

// DeliverabilityReader is the health surface these tools need. Snapshot comes
// from the pulse domain and the two health reads from the deliverability
// domain; they share one interface because they answer one question ("how is
// sending going?") and the composition root composes the two services into a
// single adapter.
type DeliverabilityReader interface {
	WorkspaceHealth(ctx context.Context, ws uuid.UUID) (WorkspaceHealth, error)
	CampaignHealth(ctx context.Context, ws, campaignID uuid.UUID) (CampaignHealth, error)
	Snapshot(ctx context.Context, ws uuid.UUID) (Snapshot, error)
}

type scoreView struct {
	Value      int             `json:"value"`
	Delivered  int             `json:"delivered"`
	Confidence string          `json:"confidence"`
	Components []componentView `json:"components"`
}

type componentView struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Measured bool     `json:"measured"`
	Penalty  int      `json:"penalty"`
	Rate     *float64 `json:"rate_pct,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

func renderScore(s deliverability.Score) scoreView {
	comps := make([]componentView, 0, len(s.Components))
	for _, c := range s.Components {
		comps = append(comps, componentView{
			Key: c.Key, Label: c.Label, Measured: c.Measured, Penalty: c.Penalty, Rate: c.Rate, Detail: c.Detail,
		})
	}
	return scoreView{Value: s.Value, Delivered: s.Delivered, Confidence: string(s.Confidence), Components: comps}
}

type riskView struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

func renderRisks(in []HealthRisk, limit int) []riskView {
	if len(in) > limit {
		in = in[:limit]
	}
	out := make([]riskView, 0, len(in))
	for _, r := range in {
		out = append(out, riskView(r))
	}
	return out
}

func deliverabilityTools(deps Deps) []Tool {
	if deps.Deliverability == nil {
		return nil
	}
	return []Tool{deliverabilityReadTool(deps.Deliverability)}
}

type deliverabilityReadArgs struct {
	baseArgs
	Method     string `json:"method"`
	CampaignID string `json:"campaign_id"`
	Limit      *int   `json:"limit"`
}

func deliverabilityReadTool(r DeliverabilityReader) Tool {
	methods := []string{methodPulse, methodWorkspace, methodCampaign}
	return Tool{
		Name: "inroad_deliverability_read",
		Description: "Read sending health. Use method=pulse first for the live snapshot — mailbox and campaign counts, today's sending against the daily cap, and the ranked list of things needing attention. " +
			"Use method=workspace for the deliverability score with its component breakdown plus the at-risk mailboxes and sending domains, " +
			"and method=campaign for one campaign's score, its auto-pause thresholds, and any pauses the circuit breaker has already triggered. " +
			"A component marked measured=false means there is no signal for it — report that as unknown, never as zero. " +
			"Use this before advising on bounce rates, spam placement or why a campaign stopped.",
		InputSchema: mustSchema(
			methodField("Which health read to perform.", methods),
			strField("campaign_id", "The campaign's id, from inroad_campaign_read. Required for method=campaign.", false),
			limitField(),
		),
		Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a deliverabilityReadArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			limit, bad := resolveLimit(a.Limit)
			if bad != nil {
				return *bad, nil
			}

			switch a.Method {
			case methodPulse:
				s, err := r.Snapshot(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("workspace pulse: %w", err)
				}
				return Ok(renderSnapshot(s, limit)), nil
			case methodWorkspace:
				h, err := r.WorkspaceHealth(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("workspace deliverability: %w", err)
				}
				return Ok(map[string]any{
					"score":             renderScore(h.Score),
					"at_risk_mailboxes": renderRisks(h.AtRiskMailboxes, limit),
					"at_risk_domains":   renderRisks(h.AtRiskDomains, limit),
				}), nil
			case methodCampaign:
				id, bad := parseID("campaign_id", a.CampaignID)
				if bad != nil {
					return *bad, nil
				}
				h, err := r.CampaignHealth(ctx, p.WorkspaceID, id)
				if err != nil {
					if isNoRecord(err) {
						return campaignMissing(a.CampaignID), nil
					}
					return Result{}, fmt.Errorf("campaign deliverability: %w", err)
				}
				return Ok(renderCampaignHealth(h, limit)), nil
			default:
				return unknownMethod(a.Method, methods), nil
			}
		},
	}
}

func renderSnapshot(s Snapshot, limit int) map[string]any {
	attention := s.Attention
	if len(attention) > limit {
		attention = attention[:limit]
	}
	rows := make([]map[string]any, 0, len(attention))
	for _, a := range attention {
		rows = append(rows, map[string]any{
			"kind": a.Kind, "severity": a.Severity, "count": a.Count, "reason": a.Reason,
		})
	}
	return map[string]any{
		"mailboxes": map[string]int64{
			"total": s.MailboxesTotal, "active": s.MailboxesActive,
			"paused": s.MailboxesPaused, "error": s.MailboxesError,
		},
		"warmup": map[string]int64{
			"pool": s.WarmupPool, "unknown": s.WarmupUnknown, "healthy": s.WarmupHealthy,
			"watch": s.WarmupWatch, "at_risk": s.WarmupAtRisk,
		},
		"campaigns": map[string]int64{
			"total": s.CampaignsTotal, "running": s.CampaignsRunning,
			"draft": s.CampaignsDraft, "paused": s.CampaignsPaused,
		},
		"contacts_total": s.ContactsTotal,
		"sending_today":  map[string]int64{"sent": s.SentToday, "daily_cap": s.DailyCap},
		"attention":      rows,
	}
}

func renderCampaignHealth(h CampaignHealth, limit int) map[string]any {
	events := h.PauseEvents
	if len(events) > limit {
		events = events[:limit]
	}
	rows := make([]map[string]any, 0, len(events))
	for _, e := range events {
		rows = append(rows, map[string]any{
			"reason": e.Reason, "metric": e.Metric, "value_pct": e.Value,
			"threshold_pct": e.Threshold, "delivered": e.Delivered, "at": rfc3339Time(e.CreatedAt),
		})
	}
	return map[string]any{
		"score":   renderScore(h.Score),
		"verdict": h.Verdict,
		"guardrails": map[string]any{
			"auto_pause_enabled":  h.AutoPauseEnabled,
			"bounce_pause_pct":    h.BouncePausePct,
			"complaint_pause_pct": h.ComplaintPausePct,
		},
		"pause_events": rows,
	}
}
