package sequence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/metrics"
)

// A pending branch condition schedules the next look at exactly RecheckAt and
// does nothing else: no claim, no send, no stop. The job also carries Skip (for
// workers that predate ConditionPending), so this proves ConditionPending is
// checked first — with Skip winning, nothing would be enqueued.
func TestAdvanceConditionPendingSchedulesRecheck(t *testing.T) {
	at := time.Date(2026, 9, 24, 14, 3, 7, 0, time.UTC)
	core := &stubCore{job: coreapi.StepSendJob{ConditionPending: true, RecheckAt: at, Skip: true}}
	snd, enq := &fakeSender{}, &fakeEnq{}
	if err := run(t, core, snd, enq); err != nil {
		t.Fatal(err)
	}
	if !enq.atCalled || !enq.at.Equal(at) {
		t.Fatalf("recheck enqueued=%v at=%v, want %v", enq.atCalled, enq.at, at)
	}
	if snd.called() || core.claimCalls != 0 || core.stopped != "" || core.finalized != nil || enq.inCalled {
		t.Fatal("a pending condition must not claim, send, stop or back off")
	}
}

// A condition wait is not a send outcome: it recurs hourly for every waiting
// enrollment, and counting it as result="deferred" would drown the capacity
// and limit defers that bucket is for. No inroad_sends_total series moves.
func TestAdvanceConditionPendingRecordsNoSendMetric(t *testing.T) {
	mtx := metrics.New()
	core := &stubCore{job: coreapi.StepSendJob{ConditionPending: true, RecheckAt: time.Now(), Skip: true}}
	if err := runWithMetrics(t, core, &fakeSender{}, &fakeEnq{}, mtx); err != nil {
		t.Fatal(err)
	}
	for _, result := range []string{"sent", "failed", "deferred", "skipped"} {
		if n := sendsCount(t, mtx, result); n != 0 {
			t.Errorf("result=%s = %v, want 0", result, n)
		}
	}
}

// An enqueue failure is returned so asynq retries: the control plane already
// stamped next_due_at, so even a lost retry is re-driven by the sweeper, but
// swallowing the error would hide a Redis outage.
func TestAdvanceConditionPendingEnqueueErrorIsReturned(t *testing.T) {
	core := &stubCore{job: coreapi.StepSendJob{ConditionPending: true, RecheckAt: time.Now(), Skip: true}}
	enq := &failingAtEnq{err: errors.New("redis down")}
	if err := run(t, core, &fakeSender{}, enq); err == nil {
		t.Fatal("an enqueue failure must surface")
	}
}

type failingAtEnq struct {
	fakeEnq
	err error
}

func (f *failingAtEnq) EnqueueAdvanceAt(context.Context, string, string, time.Time) error {
	return f.err
}
