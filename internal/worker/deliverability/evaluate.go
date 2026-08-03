// Package deliverability runs the campaign circuit breaker in the execution
// plane: it asks the control plane to re-evaluate one campaign's deliverability
// and stop it if a rate has breached its threshold.
//
// It runs as its own task, enqueued after a send is FINALISED, so the evaluation
// reads committed state and sits entirely outside the send transaction — a bug in
// the scoring cannot fail a delivery (invariant 5). All the judgement lives in the
// control plane behind Breaker; this package is the trigger and the log line.
package deliverability

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

// Breaker is the narrow coreapi capability this handler needs, defined here by
// the consumer — the same shape as maintenance.Cleaner and domainauth.Core.
//
// It is deliberately NOT a method on coreapi.Client: that interface is already 40
// methods with 13 test fakes implementing all of them, so widening it to add one
// capability would break every fake in the repo to serve one call site. The
// in-process client satisfies this at the composition root by type assertion.
type Breaker interface {
	// EvaluateCampaignBreaker re-scores one campaign and pauses it if a rate has
	// breached its threshold on a large enough sample. It reports whether THIS
	// call is the one that paused it (so only one log line claims the pause), and
	// the reason it fired.
	EvaluateCampaignBreaker(ctx context.Context, campaignID, workspaceID string) (coreapi.BreakerResult, error)
}

// EvaluateHandler returns an asynq handler for deliverability:evaluate tasks.
//
// An auto-pause is logged at WARN with the full reason — which rate, what value,
// what threshold, over what sample — because it is an unattended action taken on
// an operator's campaign, and the log is the second place (after
// campaign_pause_events) they can find out why.
func EvaluateHandler(breaker Breaker) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.DeliverabilityEvaluatePayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		res, err := breaker.EvaluateCampaignBreaker(ctx, p.CampaignID, p.WorkspaceID)
		if err != nil {
			return err
		}
		if res.Paused {
			slog.WarnContext(ctx, "campaign_auto_paused",
				"campaign_id", p.CampaignID, "reason", res.Reason, "metric", res.Metric,
				"value", res.Value, "threshold", res.Threshold, "delivered", res.Delivered,
				"trigger", "send_finalized")
		}
		return nil
	}
}
