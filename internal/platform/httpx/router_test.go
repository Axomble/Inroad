package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/inroad/inroad/internal/platform/metrics"
)

func TestHealthz(t *testing.T) {
	r := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), metrics.New())
	req := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
}

// TestNewRouterWiresMetricsMiddleware proves NewRouter's metrics middleware
// (not just the standalone metrics package tests) actually records a real
// request served through the full platform chain — route pattern, method,
// and final status code.
func TestNewRouterWiresMetricsMiddleware(t *testing.T) {
	mtx := metrics.New()
	r := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), mtx)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", http.NoBody)
	r.ServeHTTP(httptest.NewRecorder(), req)

	scrapeReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", http.NoBody)
	scrapeW := httptest.NewRecorder()
	mtx.Handler().ServeHTTP(scrapeW, scrapeReq)

	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(scrapeW.Body)
	if err != nil {
		t.Fatalf("parse exposition text: %v", err)
	}
	fam, ok := families["inroad_http_requests_total"]
	if !ok {
		t.Fatal("inroad_http_requests_total not recorded by NewRouter's middleware chain")
	}
	found := false
	for _, m := range fam.GetMetric() {
		labels := map[string]string{}
		for _, lp := range m.GetLabel() {
			labels[lp.GetName()] = lp.GetValue()
		}
		if labels["route"] == "/healthz" && labels["method"] == http.MethodGet && labels["code"] == "200" {
			found = true
			if got := m.GetCounter().GetValue(); got != 1 {
				t.Fatalf("counter = %v, want 1", got)
			}
		}
	}
	if !found {
		t.Fatalf("no sample for route=/healthz method=GET code=200; got %v", fam.GetMetric())
	}
}

// TestNewRouterMetricsMiddlewareNilSafe proves NewRouter still serves
// requests normally when passed a nil *metrics.Metrics (metrics disabled).
func TestNewRouterMetricsMiddlewareNilSafe(t *testing.T) {
	r := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), nil)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
