package warmup

import (
	"context"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
	"github.com/inroad/inroad/internal/platform/queue"
)

// TestSweepRecordsDueParticipantsScanned: ListDueWarmupMailboxes is the other
// known-unbounded scan, so its per-tick row count is the growth curve to watch.
func TestSweepRecordsDueParticipantsScanned(t *testing.T) {
	m := metrics.New()
	core := &sweepCore{due: []coreapi.MailboxRef{
		{ID: "mb-1", WorkspaceID: "ws-1"},
		{ID: "mb-2", WorkspaceID: "ws-2"},
	}}
	if err := SweepHandler(core, &fakeEnq{}, m)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	families := metricstest.Scrape(t, m)
	if got := metricstest.CounterValue(families, "inroad_sweep_rows_total", map[string]string{"kind": "warmup"}); got != 2 {
		t.Fatalf("rows = %v, want 2 (one per due participant)", got)
	}
	if got := metricstest.HistogramSampleCount(families, "inroad_sweep_seconds", map[string]string{"kind": "warmup"}); got != 1 {
		t.Fatalf("duration observations = %d, want 1", got)
	}
}

// TestSweepRecordsUnderTheWarmupKindOnly guards the sweeps against sharing a
// label: three handlers write to one metric family, so each must claim its own
// kind or their growth curves become one indistinguishable line.
func TestSweepRecordsUnderTheWarmupKindOnly(t *testing.T) {
	m := metrics.New()
	if err := SweepHandler(&sweepCore{}, &fakeEnq{}, m)(context.Background(), asynq.NewTask(queue.TaskWarmupSweep, nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}
	families := metricstest.Scrape(t, m)
	for _, other := range []string{"inbox", "enrollments"} {
		if got := metricstest.HistogramSampleCount(families, "inroad_sweep_seconds", map[string]string{"kind": other}); got != 0 {
			t.Errorf("warmup sweep wrote to kind=%q (%d observations)", other, got)
		}
	}
}
