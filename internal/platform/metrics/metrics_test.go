package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/inroad/inroad/internal/platform/metrics"
)

// scrape serves m.Handler() and parses the Prometheus exposition text into
// metric families, so a black-box test can assert on recorded values without
// reaching into Metrics' private fields.
func scrape(t *testing.T, m *metrics.Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", http.NoBody)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("metrics handler status = %d, want 200", w.Code)
	}
	// LegacyValidation matches every metric/label name this package emits
	// (snake_case, ASCII) — the exposition format's UTF-8 name scheme is
	// prometheus/common's newer default but has no bearing on names this
	// simple.
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(w.Body)
	if err != nil {
		t.Fatalf("parse exposition text: %v", err)
	}
	return families
}

func findSample(t *testing.T, families map[string]*dto.MetricFamily, name string, labels map[string]string) *dto.Metric {
	t.Helper()
	fam, ok := families[name]
	if !ok {
		t.Fatalf("metric family %q not found; have %v", name, familyNames(families))
	}
	for _, metric := range fam.GetMetric() {
		if labelsMatch(metric, labels) {
			return metric
		}
	}
	t.Fatalf("no sample of %q with labels %v; got %v", name, labels, fam.GetMetric())
	return nil
}

func labelsMatch(metric *dto.Metric, want map[string]string) bool {
	if len(metric.GetLabel()) != len(want) {
		return false
	}
	for _, lp := range metric.GetLabel() {
		if want[lp.GetName()] != lp.GetValue() {
			return false
		}
	}
	return true
}

func familyNames(families map[string]*dto.MetricFamily) []string {
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	return names
}

func TestHTTPMiddlewareRecordsRoutePatternMethodAndCode(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware())
	r.Get("/widgets/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/widgets/123", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}

	families := scrape(t, m)
	sample := findSample(t, families, "inroad_http_requests_total", map[string]string{
		"route": "/widgets/{id}", "method": "GET", "code": "201",
	})
	if got := sample.GetCounter().GetValue(); got != 1 {
		t.Fatalf("counter value = %v, want 1", got)
	}
}

// TestHTTPMiddlewareObservesDuration proves the latency histogram records one
// observation per request, keyed by route pattern + method (no status code —
// a distribution shouldn't fragment per exact code).
func TestHTTPMiddlewareObservesDuration(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware())
	r.Get("/widgets/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/widgets/123", http.NoBody)
	r.ServeHTTP(httptest.NewRecorder(), req)

	families := scrape(t, m)
	sample := findSample(t, families, "inroad_http_request_seconds", map[string]string{
		"route": "/widgets/{id}", "method": "GET",
	})
	if got := sample.GetHistogram().GetSampleCount(); got != 1 {
		t.Fatalf("histogram sample count = %d, want 1", got)
	}
}

// TestHTTPMiddlewareCollapsesUnmatchedRoutes proves a request that never
// matched a registered chi pattern (a genuine 404) is NOT labeled with the raw
// URL path — that would let a path-fuzzer or per-resource-id URL mint one
// series per attempt, exactly the cardinality leak the route-PATTERN
// requirement exists to prevent.
func TestHTTPMiddlewareCollapsesUnmatchedRoutes(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware())
	r.Get("/known", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, path := range []string{"/does-not-exist/1", "/does-not-exist/2"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	families := scrape(t, m)
	fam, ok := families["inroad_http_requests_total"]
	if !ok {
		t.Fatal("inroad_http_requests_total not found")
	}
	for _, metric := range fam.GetMetric() {
		for _, lp := range metric.GetLabel() {
			if lp.GetName() == "route" && (lp.GetValue() == "/does-not-exist/1" || lp.GetValue() == "/does-not-exist/2") {
				t.Fatalf("route label leaked the raw request path: %v", metric)
			}
		}
	}
	sample := findSample(t, families, "inroad_http_requests_total", map[string]string{
		"route": "unmatched", "method": "GET", "code": "404",
	})
	if got := sample.GetCounter().GetValue(); got != 2 {
		t.Fatalf("unmatched counter = %v, want 2 (both fuzzed paths collapse onto one series)", got)
	}
}

func TestSendFinalizedIncrementsWithLabels(t *testing.T) {
	m := metrics.New()
	m.SendFinalized("campaign", "sent")
	m.SendFinalized("campaign", "sent")
	m.SendFinalized("warmup", "failed")

	families := scrape(t, m)
	sent := findSample(t, families, "inroad_sends_total", map[string]string{"kind": "campaign", "result": "sent"})
	if got := sent.GetCounter().GetValue(); got != 2 {
		t.Fatalf("campaign/sent = %v, want 2", got)
	}
	failed := findSample(t, families, "inroad_sends_total", map[string]string{"kind": "warmup", "result": "failed"})
	if got := failed.GetCounter().GetValue(); got != 1 {
		t.Fatalf("warmup/failed = %v, want 1", got)
	}
}

// TestNilMetricsNoOps proves every method on a nil *Metrics is a safe no-op —
// the worker's finalize points and the HTTP middleware chain must not need
// their own "metrics == nil" guard.
func TestNilMetricsNoOps(t *testing.T) {
	var m *metrics.Metrics

	m.SendFinalized("campaign", "sent") // must not panic

	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware())
	r.Get("/widgets/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/widgets/123", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req) // must not panic, and must still reach the handler
	if w.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 (nil middleware must be a pure pass-through)", w.Code)
	}

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", http.NoBody)) // must not panic
	if rec.Code == http.StatusOK {
		t.Fatalf("nil Metrics has no registry to serve; want a non-200, got %d with body %q", rec.Code, rec.Body.String())
	}
}
