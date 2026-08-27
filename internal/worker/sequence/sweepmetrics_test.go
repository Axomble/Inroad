package sequence

import (
	"context"
	"errors"
	"testing"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
)

// TestSweepRecordsCandidateRowsAndDuration: the enrollment sweeper's candidate
// count is the per-tick cost of the due-enrollment scan.
func TestSweepRecordsCandidateRowsAndDuration(t *testing.T) {
	m := metrics.New()
	core := &sweepCore{due: []coreapi.DueEnrollment{
		{EnrollmentID: "e1", WorkspaceID: "w"},
		{EnrollmentID: "e2", WorkspaceID: "w"},
	}}
	if err := SweepHandler(core, &countEnq{}, m)(context.Background(), sweepTask()); err != nil {
		t.Fatal(err)
	}

	families := metricstest.Scrape(t, m)
	if got := metricstest.CounterValue(families, "inroad_sweep_rows_total", map[string]string{"kind": "enrollments"}); got != 2 {
		t.Fatalf("rows = %v, want 2", got)
	}
	if got := metricstest.HistogramSampleCount(families, "inroad_sweep_seconds", map[string]string{"kind": "enrollments"}); got != 1 {
		t.Fatalf("duration observations = %d, want 1", got)
	}
}

// TestSweepTimesAnEmptyTick: the early return on "nothing due" must still
// record the scan, or a sweep that grows expensive while returning nothing
// stays invisible.
func TestSweepTimesAnEmptyTick(t *testing.T) {
	m := metrics.New()
	if err := SweepHandler(&sweepCore{}, &countEnq{}, m)(context.Background(), sweepTask()); err != nil {
		t.Fatal(err)
	}
	if got := metricstest.HistogramSampleCount(metricstest.Scrape(t, m), "inroad_sweep_seconds", map[string]string{"kind": "enrollments"}); got != 1 {
		t.Fatalf("duration observations = %d, want 1 (an empty tick is still a scan)", got)
	}
}

// TestSweepRecordsNothingWhenTheScanFails: a failed scan has no row count, and
// its duration must not dilute the histogram.
func TestSweepRecordsNothingWhenTheScanFails(t *testing.T) {
	m := metrics.New()
	core := &sweepCore{err: errors.New("db down")}
	if err := SweepHandler(core, &countEnq{}, m)(context.Background(), sweepTask()); err == nil {
		t.Fatal("expected the scan error to propagate")
	}
	if got := metricstest.HistogramSampleCount(metricstest.Scrape(t, m), "inroad_sweep_seconds", map[string]string{"kind": "enrollments"}); got != 0 {
		t.Fatalf("duration observations = %d, want 0", got)
	}
}
