package inbox

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
)

// TestSweepRecordsRowsScanned proves the inbox sweep reports how many mailboxes
// its unbounded scan returned. That number IS the growth curve — without it,
// ListActiveMailboxes going from 50 to 50,000 rows is invisible until it times
// out.
func TestSweepRecordsRowsScanned(t *testing.T) {
	m := metrics.New()
	core := &stubCore{mailboxes: []coreapi.MailboxRef{
		{ID: "m1", WorkspaceID: "w1"}, {ID: "m2", WorkspaceID: "w1"}, {ID: "m3", WorkspaceID: "w2"},
	}}
	h := SweepHandler(core, &fakeEnqueuer{}, m)
	if err := h(context.Background(), asynq.NewTask("inbox:sweep", nil)); err != nil {
		t.Fatalf("handler: %v", err)
	}

	families := metricstest.Scrape(t, m)
	if got := metricstest.CounterValue(families, "inroad_sweep_rows_total", map[string]string{"kind": "inbox"}); got != 3 {
		t.Fatalf("rows = %v, want 3 (one per active mailbox)", got)
	}
	if got := metricstest.HistogramSampleCount(families, "inroad_sweep_seconds", map[string]string{"kind": "inbox"}); got != 1 {
		t.Fatalf("duration observations = %d, want 1", got)
	}
}

// TestSweepRecordsNothingWhenTheScanFails: folding a failed scan's (short)
// duration into the histogram would drag the sweep's apparent cost DOWN during
// exactly the incident an operator is investigating.
func TestSweepRecordsNothingWhenTheScanFails(t *testing.T) {
	m := metrics.New()
	core := &stubCore{listErr: errors.New("db down")}
	h := SweepHandler(core, &fakeEnqueuer{}, m)
	if err := h(context.Background(), asynq.NewTask("inbox:sweep", nil)); err == nil {
		t.Fatal("expected the scan error to propagate")
	}

	families := metricstest.Scrape(t, m)
	if got := metricstest.HistogramSampleCount(families, "inroad_sweep_seconds", map[string]string{"kind": "inbox"}); got != 0 {
		t.Fatalf("duration observations = %d, want 0 (a failed scan is not an observation)", got)
	}
}
