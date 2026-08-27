package metrics_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
)

// fakeInspector is the QueueInspector seam's test double. It counts calls so a
// test can prove the collector reads ON SCRAPE rather than once at
// registration.
type fakeInspector struct {
	depths []metrics.QueueDepth
	err    error
	calls  int
}

func (f *fakeInspector) QueueDepths() ([]metrics.QueueDepth, error) {
	f.calls++
	return f.depths, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestRegisterQueueReportsDepthPerQueueAndState(t *testing.T) {
	m := metrics.New()
	insp := &fakeInspector{depths: []metrics.QueueDepth{
		{Name: "default", Pending: 3, Active: 2, Scheduled: 5, Retry: 1, Dead: 4},
		{Name: "w:node-1", Pending: 9},
	}}
	if err := m.RegisterQueue(insp, discardLogger()); err != nil {
		t.Fatalf("register queue: %v", err)
	}

	families := metricstest.Scrape(t, m)
	for _, tc := range []struct {
		queue, state string
		want         float64
	}{
		{"default", "pending", 3},
		{"default", "active", 2},
		{"default", "scheduled", 5},
		{"default", "retry", 1},
		{"default", "dead", 4},
		{"w:node-1", "pending", 9},
		// A queue with no backlog in a state still reports an explicit zero,
		// which is what lets an alert distinguish "empty" from "not scraped".
		{"w:node-1", "retry", 0},
	} {
		got := metricstest.GaugeValue(families, "inroad_queue_depth", map[string]string{
			"queue": tc.queue, "state": tc.state,
		})
		if got != tc.want {
			t.Errorf("depth{queue=%q,state=%q} = %v, want %v", tc.queue, tc.state, got, tc.want)
		}
	}
}

// TestQueueCollectorReadsOnEveryScrape is the "collector, not poller" contract:
// two scrapes must consult the inspector twice, and the second must report the
// NEW value rather than the one cached at registration.
func TestQueueCollectorReadsOnEveryScrape(t *testing.T) {
	m := metrics.New()
	insp := &fakeInspector{depths: []metrics.QueueDepth{{Name: "default", Pending: 1}}}
	if err := m.RegisterQueue(insp, discardLogger()); err != nil {
		t.Fatalf("register queue: %v", err)
	}

	metricstest.Scrape(t, m)
	insp.depths = []metrics.QueueDepth{{Name: "default", Pending: 42}}
	families := metricstest.Scrape(t, m)

	if insp.calls != 2 {
		t.Fatalf("inspector consulted %d times across 2 scrapes, want 2", insp.calls)
	}
	got := metricstest.GaugeValue(families, "inroad_queue_depth", map[string]string{
		"queue": "default", "state": "pending",
	})
	if got != 42 {
		t.Fatalf("pending = %v, want 42 (the value at the second scrape)", got)
	}
}

// TestQueueCollectorEmitsNothingAndLogsOnInspectionFailure: a Redis outage must
// leave a GAP, not a series frozen at its last good value — a stale zero reads
// as "backlog is fine" during exactly the incident that matters.
func TestQueueCollectorEmitsNothingAndLogsOnInspectionFailure(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	m := metrics.New()
	insp := &fakeInspector{err: errors.New("redis unreachable")}
	if err := m.RegisterQueue(insp, logger); err != nil {
		t.Fatalf("register queue: %v", err)
	}

	families := metricstest.Scrape(t, m)
	if fam, ok := families["inroad_queue_depth"]; ok && len(fam.GetMetric()) > 0 {
		t.Fatalf("failed inspection must emit no series, got %d", len(fam.GetMetric()))
	}
	if !strings.Contains(logs.String(), "redis unreachable") {
		t.Fatalf("inspection failure was swallowed; logs = %q", logs.String())
	}
}

// TestQueueDepthLabelsAreQueueAndStateOnly is the cardinality guard: the only
// dimensions permitted are the (bounded) queue name and the fixed state. A
// workspace/campaign/mailbox label would be unbounded.
func TestQueueDepthLabelsAreQueueAndStateOnly(t *testing.T) {
	m := metrics.New()
	if err := m.RegisterQueue(&fakeInspector{depths: []metrics.QueueDepth{{Name: "default"}}}, discardLogger()); err != nil {
		t.Fatalf("register queue: %v", err)
	}
	fam, ok := metricstest.Scrape(t, m)["inroad_queue_depth"]
	if !ok {
		t.Fatal("inroad_queue_depth missing")
	}
	for _, sample := range fam.GetMetric() {
		for _, lp := range sample.GetLabel() {
			if lp.GetName() != "queue" && lp.GetName() != "state" {
				t.Errorf("unexpected label %q on inroad_queue_depth (cardinality)", lp.GetName())
			}
		}
	}
}

func TestRegisterQueueNilReceiverAndNilInspectorAreNoOps(t *testing.T) {
	var nilMetrics *metrics.Metrics
	if err := nilMetrics.RegisterQueue(&fakeInspector{}, discardLogger()); err != nil {
		t.Fatalf("nil Metrics RegisterQueue = %v, want nil", err)
	}

	m := metrics.New()
	if err := m.RegisterQueue(nil, discardLogger()); err != nil {
		t.Fatalf("RegisterQueue(nil inspector) = %v, want nil", err)
	}
	if _, ok := metricstest.Scrape(t, m)["inroad_queue_depth"]; ok {
		t.Fatal("no inspector registered, so no queue series should exist")
	}
}

// TestRegisterQueueWithNilLoggerStillScrapes: a caller that forgets a logger
// must not get a collector that panics on the first inspection failure.
func TestRegisterQueueWithNilLoggerStillScrapes(t *testing.T) {
	m := metrics.New()
	if err := m.RegisterQueue(&fakeInspector{err: errors.New("boom")}, nil); err != nil {
		t.Fatalf("register queue: %v", err)
	}
	metricstest.Scrape(t, m) // must not panic
}
