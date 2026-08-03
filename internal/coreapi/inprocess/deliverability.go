package inprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
)

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
	return coreapi.BreakerResult{
		Paused:    out.Paused,
		Reason:    out.Verdict.Reason,
		Metric:    out.Verdict.Metric,
		Value:     out.Verdict.Value,
		Threshold: out.Verdict.Threshold,
		Delivered: out.Verdict.Delivered,
	}, nil
}
