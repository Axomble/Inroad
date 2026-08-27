// Package metricstest is test-only support for asserting on a
// *metrics.Metrics' exposed Prometheus registry from ANOTHER package's
// tests, without reaching into Metrics' private fields (New/Handler/
// HTTPMiddleware/SendFinalized are the whole production surface — nothing
// here is part of it). Every helper scrapes the real m.Handler() and parses
// the real Prometheus exposition text via the real client_golang/
// client_model libraries, so a test reads exactly what a live Prometheus
// server would — never a hand-rolled parser standing in for one.
//
// Import this package ONLY from _test.go files.
package metricstest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/inroad/inroad/internal/platform/metrics"
)

// Scrape serves m.Handler() and parses its exposition text into metric
// families, keyed by metric name. LegacyValidation matches every name this
// repo's metrics emit (snake_case, ASCII) — the exposition format's newer
// UTF-8 name scheme has no bearing on names this simple.
func Scrape(t *testing.T, m *metrics.Metrics) map[string]*dto.MetricFamily {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", http.NoBody)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("metrics handler status = %d, want 200", w.Code)
	}
	parser := expfmt.NewTextParser(model.LegacyValidation)
	families, err := parser.TextToMetricFamilies(w.Body)
	if err != nil {
		t.Fatalf("parse exposition text: %v", err)
	}
	return families
}

// FindMetric returns the sample in family `name` whose labels match `labels`
// EXACTLY (same set, same values — not a subset), or nil if no such family or
// sample exists. A nil return is not itself a test failure: a metric that was
// never incremented/observed simply has no series yet in a fresh registry,
// which callers asserting "this bucket must stay at zero" need to be able to
// express without a spurious Fatal.
func FindMetric(families map[string]*dto.MetricFamily, name string, labels map[string]string) *dto.Metric {
	fam, ok := families[name]
	if !ok {
		return nil
	}
	for _, sample := range fam.GetMetric() {
		if labelsMatch(sample, labels) {
			return sample
		}
	}
	return nil
}

// CounterValue returns the value of the counter sample in family `name`
// matching `labels` exactly, or 0 if no such sample exists yet.
func CounterValue(families map[string]*dto.MetricFamily, name string, labels map[string]string) float64 {
	m := FindMetric(families, name, labels)
	if m == nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

// GaugeValue returns the value of the gauge sample in family `name` matching
// `labels` exactly, or 0 if no such sample exists yet. Pass nil labels for an
// unlabeled gauge (the pool collector's series are deliberately unlabeled).
func GaugeValue(families map[string]*dto.MetricFamily, name string, labels map[string]string) float64 {
	m := FindMetric(families, name, labels)
	if m == nil {
		return 0
	}
	return m.GetGauge().GetValue()
}

// HistogramSampleCount returns the observation count of the histogram sample
// in family `name` matching `labels` exactly, or 0 if no such sample exists
// yet.
func HistogramSampleCount(families map[string]*dto.MetricFamily, name string, labels map[string]string) uint64 {
	m := FindMetric(families, name, labels)
	if m == nil {
		return 0
	}
	return m.GetHistogram().GetSampleCount()
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
