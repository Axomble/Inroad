package inprocess

import (
	"context"
	"strconv"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/audit"
)

// recordBreakerPause audits an AUTOMATIC pause, attributed to the breaker
// rather than to any person, so the viewer can tell "someone paused this" from
// "the system paused this" — campaigns.status alone cannot. EvaluateBreaker
// reports Paused only on the one evaluation that paused, so this records once per
// pause. Best-effort like every campaign state event (security.md invariant
// 82): the pause has already committed and must not be undone by an audit
// outage. A client built without a pool (unit tests) records nothing.
func (c client) recordBreakerPause(ctx context.Context, ws, campaignID uuid.UUID, reason, metric string, value, threshold float64) {
	if c.pool == nil {
		return
	}
	ev := audit.New(ctx, ws, audit.ActionCampaignPaused, "campaign", campaignID.String(), audit.Metadata{
		"reason":    reason,
		"metric":    metric,
		"value":     strconv.FormatFloat(value, 'f', 4, 64),
		"threshold": strconv.FormatFloat(threshold, 'f', 4, 64),
	})
	ev.Actor = audit.SystemActor("deliverability_breaker")
	audit.Emit(ctx, audit.NewPgRecorder(c.pool), ev)
}

// EvaluateCampaignBreaker re-scores one campaign and pauses it when a rate has
// breached its threshold on a large enough sample.
//
// It is the control⇄execution seam for the circuit breaker, and it is NOT part of
// coreapi.Client: the worker consumes it through its own one-method interface
// (worker/deliverability.Breaker), which this satisfies by type assertion at the
// composition root — the maintenance.Cleaner pattern. See coreapi.BreakerResult
// for why widening Client was the wrong trade.
//
// All the judgement is delegated to the app/deliverability service the client
// composes, exactly as the MarkStep* methods delegate to app/enrollment: there is
// one implementation of the breaker and both the API and the worker reach it here.
//
// workspaceID is pinned in every statement the evaluation runs. A malformed id
// cannot identify any campaign, so it is a plain error rather than a lookup.
func (c client) EvaluateCampaignBreaker(ctx context.Context, campaignID, workspaceID string) (coreapi.BreakerResult, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.BreakerResult{}, err
	}
	cid, err := uuid.Parse(campaignID)
	if err != nil {
		return coreapi.BreakerResult{}, err
	}
	out, err := c.breaker.EvaluateBreaker(ctx, ws, cid)
	if err != nil {
		return coreapi.BreakerResult{}, err
	}
	if out.Paused {
		c.recordBreakerPause(ctx, ws, cid, out.Verdict.Reason, out.Verdict.Metric, out.Verdict.Value, out.Verdict.Threshold)
	}
	return coreapi.BreakerResult{
		Paused:    out.Paused,
		Reason:    out.Verdict.Reason,
		Metric:    out.Verdict.Metric,
		Value:     out.Verdict.Value,
		Threshold: out.Verdict.Threshold,
		Delivered: out.Verdict.Delivered,
	}, nil
}
