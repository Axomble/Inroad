package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/platform/metrics"
)

// recordOneRequest drives one request through the HTTP middleware so the two
// pre-existing http families materialise alongside the new ones.
func recordOneRequest(t *testing.T, m *metrics.Metrics) {
	t.Helper()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware())
	r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ping", http.NoBody))
}

// TestExpositionListsEveryInroadSeries pins the complete set of inroad_*
// metric names a scrape exposes. An operator's dashboards and alerts are
// written against these exact names, so adding, renaming or dropping one
// should be a deliberate edit here rather than a silent break in production.
func TestExpositionListsEveryInroadSeries(t *testing.T) {
	m := metrics.New()
	// Touch every recording path so each family actually materialises: a
	// *Vec with no observed label combination exposes nothing at all, which is
	// correct Prometheus behaviour and the reason each one needs a real call.
	recordOneRequest(t, m)
	m.SendFinalized("campaign", "sent")
	m.SendClaimed("step", metrics.ClaimOutcomeWon)
	m.SweepCompleted("inbox", 1, time.Second)
	if err := m.RegisterPool(realPoolStat(t)); err != nil {
		t.Fatalf("register pool: %v", err)
	}
	if err := m.RegisterQueue(&fakeInspector{depths: []metrics.QueueDepth{{Name: "default"}}}, discardLogger()); err != nil {
		t.Fatalf("register queue: %v", err)
	}

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", http.NoBody)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, req)

	var got []string
	for _, line := range strings.Split(w.Body.String(), "\n") {
		name, ok := strings.CutPrefix(line, "# TYPE ")
		if !ok {
			continue
		}
		name, _, _ = strings.Cut(name, " ")
		if strings.HasPrefix(name, "inroad_") {
			got = append(got, name)
		}
	}
	sort.Strings(got)

	want := []string{
		"inroad_db_pool_acquire_seconds_total",
		"inroad_db_pool_acquire_total",
		"inroad_db_pool_acquired_conns",
		"inroad_db_pool_canceled_acquire_total",
		"inroad_db_pool_empty_acquire_total",
		"inroad_db_pool_idle_conns",
		"inroad_db_pool_max_conns",
		"inroad_db_pool_total_conns",
		// The three that predate this work; their meaning is unchanged.
		"inroad_http_request_seconds",
		"inroad_http_requests_total",
		"inroad_queue_depth",
		"inroad_send_claims_total",
		"inroad_sends_total",
		"inroad_sweep_rows_total",
		"inroad_sweep_seconds",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("exposed inroad_* series:\n got %v\nwant %v", got, want)
	}
}
