package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMetricsHandler stands in for mtx.Handler(): it answers every path,
// exactly like promhttp.HandlerFor does, so these tests exercise routing
// without pulling in the metrics package.
func fakeMetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics-ok"))
	})
}

// TestMetricsMuxPprofDisabledServesOnlyMetrics proves the default (disabled)
// posture: with PprofEnabled=false, /debug/pprof/* falls through to the
// metrics handler like any other path, exactly as it did before this mux
// existed — so a self-hoster who never sets INROAD_PPROF_ENABLED sees
// byte-for-byte the old behavior.
func TestMetricsMuxPprofDisabledServesOnlyMetrics(t *testing.T) {
	mux := MetricsMux(fakeMetricsHandler(), false)

	for _, path := range []string{"/", "/metrics", "/debug/pprof/", "/debug/pprof/heap"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody))
		if w.Code != http.StatusOK || w.Body.String() != "metrics-ok" {
			t.Fatalf("path %q: got status=%d body=%q, want the metrics handler's response", path, w.Code, w.Body.String())
		}
	}
}

// TestMetricsMuxPprofEnabledMountsDebugRoutes proves the opt-in: with
// PprofEnabled=true, /debug/pprof/* is served by net/http/pprof instead of
// falling through to metrics, while every other path is unaffected.
func TestMetricsMuxPprofEnabledMountsDebugRoutes(t *testing.T) {
	mux := MetricsMux(fakeMetricsHandler(), true)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/debug/pprof/", http.NoBody))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "/debug/pprof/") {
		t.Fatalf("pprof index: got status=%d body=%q, want pprof's own index page", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/debug/pprof/cmdline", http.NoBody))
	if w.Code != http.StatusOK {
		t.Fatalf("pprof cmdline: got status=%d, want 200", w.Code)
	}

	// Metrics still answers everything pprof doesn't own.
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody))
	if w.Code != http.StatusOK || w.Body.String() != "metrics-ok" {
		t.Fatalf("root: got status=%d body=%q, want the metrics handler's response", w.Code, w.Body.String())
	}
}
