package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
)

func TestHealthz(t *testing.T) {
	r := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)), metrics.New())
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/healthz", http.NoBody)
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

	families := metricstest.Scrape(t, mtx)
	got := metricstest.CounterValue(families, "inroad_http_requests_total", map[string]string{
		"route": "/healthz", "method": http.MethodGet, "code": "200",
	})
	if got != 1 {
		t.Fatalf("counter = %v, want 1", got)
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
