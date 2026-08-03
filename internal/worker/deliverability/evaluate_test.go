package deliverability

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

// fakeBreaker records what it was asked to evaluate. Two methods short of the
// whole coreapi.Client surface — which is the point of the narrow interface.
type fakeBreaker struct {
	calls []string
	res   coreapi.BreakerResult
	err   error
}

func (f *fakeBreaker) EvaluateCampaignBreaker(_ context.Context, campaignID, workspaceID string) (coreapi.BreakerResult, error) {
	f.calls = append(f.calls, campaignID+"/"+workspaceID)
	return f.res, f.err
}

func task(t *testing.T, campaignID, workspaceID string) *asynq.Task {
	t.Helper()
	b, err := json.Marshal(queue.DeliverabilityEvaluatePayload{
		CampaignID: campaignID, WorkspaceID: workspaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return asynq.NewTask(queue.TaskDeliverabilityEvaluate, b)
}

// The handler passes BOTH ids through, so every statement the evaluation runs is
// workspace-pinned rather than trusting the campaign UUID alone.
func TestEvaluatePassesBothIDs(t *testing.T) {
	breaker := &fakeBreaker{}
	err := EvaluateHandler(breaker)(context.Background(), task(t, "campaign-1", "ws-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(breaker.calls) != 1 || breaker.calls[0] != "campaign-1/ws-1" {
		t.Errorf("calls = %v, want [campaign-1/ws-1]", breaker.calls)
	}
}

// A pause is reported without error: the campaign stopping is the SUCCESS case of
// this task, not a failure asynq should retry.
func TestEvaluateSucceedsWhenItPauses(t *testing.T) {
	breaker := &fakeBreaker{res: coreapi.BreakerResult{
		Paused: true, Reason: "bounce_spike", Metric: "bounce_rate",
		Value: 9.2, Threshold: 8, Delivered: 218,
	}}
	if err := EvaluateHandler(breaker)(context.Background(), task(t, "c", "w")); err != nil {
		t.Fatalf("a successful auto-pause returned an error: %v", err)
	}
}

// An evaluation failure IS returned, so asynq retries it. The send it followed is
// already durable and finalised, so retrying costs nothing and a campaign that
// should have stopped gets another chance to.
func TestEvaluateReturnsTheBreakersError(t *testing.T) {
	want := errors.New("boom")
	if err := EvaluateHandler(&fakeBreaker{err: want})(context.Background(), task(t, "c", "w")); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestEvaluateRejectsAMalformedPayload(t *testing.T) {
	breaker := &fakeBreaker{}
	err := EvaluateHandler(breaker)(context.Background(),
		asynq.NewTask(queue.TaskDeliverabilityEvaluate, []byte("{not json")))
	if err == nil {
		t.Fatal("a malformed payload was accepted")
	}
	if len(breaker.calls) != 0 {
		t.Errorf("evaluated %v from an undecodable payload", breaker.calls)
	}
}
