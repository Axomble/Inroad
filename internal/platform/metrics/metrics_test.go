package metrics_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/metrics/metricstest"
)

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

	families := metricstest.Scrape(t, m)
	got := metricstest.CounterValue(families, "inroad_http_requests_total", map[string]string{
		"route": "/widgets/{id}", "method": "GET", "code": "201",
	})
	if got != 1 {
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

	families := metricstest.Scrape(t, m)
	got := metricstest.HistogramSampleCount(families, "inroad_http_request_seconds", map[string]string{
		"route": "/widgets/{id}", "method": "GET",
	})
	if got != 1 {
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

	families := metricstest.Scrape(t, m)
	fam, ok := families["inroad_http_requests_total"]
	if !ok {
		t.Fatal("inroad_http_requests_total not found")
	}
	for _, sample := range fam.GetMetric() {
		for _, lp := range sample.GetLabel() {
			if lp.GetName() == "route" && (lp.GetValue() == "/does-not-exist/1" || lp.GetValue() == "/does-not-exist/2") {
				t.Fatalf("route label leaked the raw request path: %v", sample)
			}
		}
	}
	got := metricstest.CounterValue(families, "inroad_http_requests_total", map[string]string{
		"route": "unmatched", "method": "GET", "code": "404",
	})
	if got != 2 {
		t.Fatalf("unmatched counter = %v, want 2 (both fuzzed paths collapse onto one series)", got)
	}
}

func TestSendFinalizedIncrementsWithLabels(t *testing.T) {
	m := metrics.New()
	m.SendFinalized("campaign", "sent")
	m.SendFinalized("campaign", "sent")
	m.SendFinalized("warmup", "failed")

	families := metricstest.Scrape(t, m)
	if got := metricstest.CounterValue(families, "inroad_sends_total", map[string]string{"kind": "campaign", "result": "sent"}); got != 2 {
		t.Fatalf("campaign/sent = %v, want 2", got)
	}
	if got := metricstest.CounterValue(families, "inroad_sends_total", map[string]string{"kind": "warmup", "result": "failed"}); got != 1 {
		t.Fatalf("warmup/failed = %v, want 1", got)
	}
}

// TestSendClaimedSeparatesWonFromReclaimed is the point of the claim counter:
// a reclaimed lease (a worker died mid-send) must land on its OWN series, not
// be folded into "won", or the single best send-path health signal is invisible.
func TestSendClaimedSeparatesWonFromReclaimed(t *testing.T) {
	m := metrics.New()
	m.SendClaimed("step", metrics.ClaimOutcomeWon)
	m.SendClaimed("step", metrics.ClaimOutcomeWon)
	m.SendClaimed("step", metrics.ClaimOutcomeReclaimed)
	m.SendClaimed("step", metrics.ClaimOutcomeLost)

	families := metricstest.Scrape(t, m)
	for outcome, want := range map[string]float64{
		metrics.ClaimOutcomeWon:         2,
		metrics.ClaimOutcomeReclaimed:   1,
		metrics.ClaimOutcomeLost:        1,
		metrics.ClaimOutcomeAlreadySent: 0,
		metrics.ClaimOutcomeDeferred:    0,
	} {
		got := metricstest.CounterValue(families, "inroad_send_claims_total", map[string]string{
			"kind": "step", "outcome": outcome,
		})
		if got != want {
			t.Errorf("claims{outcome=%q} = %v, want %v", outcome, got, want)
		}
	}
}

// TestSweepCompletedRecordsDurationAndRows: one call must move BOTH the
// duration histogram and the rows counter, since rows-per-sweep (the growth
// curve of the unbounded scans) is only derivable from the pair.
func TestSweepCompletedRecordsDurationAndRows(t *testing.T) {
	m := metrics.New()
	m.SweepCompleted("inbox", 40, 250*time.Millisecond)
	m.SweepCompleted("inbox", 60, 750*time.Millisecond)
	m.SweepCompleted("warmup", 5, time.Second)

	families := metricstest.Scrape(t, m)
	if got := metricstest.HistogramSampleCount(families, "inroad_sweep_seconds", map[string]string{"kind": "inbox"}); got != 2 {
		t.Errorf("inbox sweep observations = %d, want 2", got)
	}
	if got := metricstest.CounterValue(families, "inroad_sweep_rows_total", map[string]string{"kind": "inbox"}); got != 100 {
		t.Errorf("inbox rows = %v, want 100 (40+60)", got)
	}
	if got := metricstest.CounterValue(families, "inroad_sweep_rows_total", map[string]string{"kind": "warmup"}); got != 5 {
		t.Errorf("warmup rows = %v, want 5", got)
	}
}

// TestSweepCompletedWithZeroRowsStillTimesTheSweep: an empty sweep is still a
// sweep, and its cost is exactly what an operator wants to see when the scan
// gets expensive without returning anything.
func TestSweepCompletedWithZeroRowsStillTimesTheSweep(t *testing.T) {
	m := metrics.New()
	m.SweepCompleted("enrollments", 0, 5*time.Second)

	families := metricstest.Scrape(t, m)
	if got := metricstest.HistogramSampleCount(families, "inroad_sweep_seconds", map[string]string{"kind": "enrollments"}); got != 1 {
		t.Fatalf("observations = %d, want 1 (an empty sweep is still timed)", got)
	}
	if got := metricstest.CounterValue(families, "inroad_sweep_rows_total", map[string]string{"kind": "enrollments"}); got != 0 {
		t.Fatalf("rows = %v, want 0", got)
	}
}

// TestSweepCompletedIgnoresNegativeRows: a Prometheus counter can only go up,
// so a negative count from an upstream arithmetic slip must be dropped rather
// than permanently corrupting the series.
func TestSweepCompletedIgnoresNegativeRows(t *testing.T) {
	m := metrics.New()
	m.SweepCompleted("inbox", 10, time.Second)
	m.SweepCompleted("inbox", -5, time.Second)

	families := metricstest.Scrape(t, m)
	if got := metricstest.CounterValue(families, "inroad_sweep_rows_total", map[string]string{"kind": "inbox"}); got != 10 {
		t.Fatalf("rows = %v, want 10 (the negative count must be ignored, never subtracted)", got)
	}
}

// TestNewRegistersRuntimeAndProcessCollectors pins the free-and-standard
// collectors being present on every process's registry without any wiring.
func TestNewRegistersRuntimeAndProcessCollectors(t *testing.T) {
	families := metricstest.Scrape(t, metrics.New())
	// go_goroutines comes from the Go collector; process_start_time_seconds
	// from the process collector. Both are stable, documented series names.
	for _, name := range []string{"go_goroutines", "go_memstats_alloc_bytes", "process_start_time_seconds"} {
		if _, ok := families[name]; !ok {
			t.Errorf("missing %q — the runtime/process collectors are not registered", name)
		}
	}
}

// TestNoTenantLabelsAnywhere is the repo-wide cardinality guard: NO series this
// registry exposes may carry a per-tenant dimension. Unbounded label
// cardinality kills the metrics backend, and per-workspace behaviour belongs in
// logs and the DB instead.
func TestNoTenantLabelsAnywhere(t *testing.T) {
	m := metrics.New()
	m.SendFinalized("campaign", "sent")
	m.SendClaimed("step", metrics.ClaimOutcomeWon)
	m.SweepCompleted("inbox", 1, time.Second)

	banned := map[string]bool{
		"workspace_id": true, "workspace": true, "tenant_id": true, "tenant": true,
		"campaign_id": true, "campaign": true, "mailbox_id": true, "mailbox": true,
		"contact_id": true, "contact": true, "enrollment_id": true, "send_id": true,
	}
	for name, fam := range metricstest.Scrape(t, m) {
		for _, sample := range fam.GetMetric() {
			for _, lp := range sample.GetLabel() {
				if banned[lp.GetName()] {
					t.Errorf("%s carries banned high-cardinality label %q", name, lp.GetName())
				}
			}
		}
	}
}

// TestNilMetricsNoOps proves every method on a nil *Metrics is a safe no-op —
// the worker's finalize points and the HTTP middleware chain must not need
// their own "metrics == nil" guard.
func TestNilMetricsNoOps(t *testing.T) {
	var m *metrics.Metrics

	// Every recording method, on a nil receiver. The claim path in particular
	// runs with a nil *Metrics in every unit test and integration test that
	// builds an inprocess client without WithMetrics.
	m.SendFinalized("campaign", "sent")
	m.SendClaimed("step", metrics.ClaimOutcomeWon)
	m.SweepCompleted("inbox", 12, 3*time.Second)

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

// TestHTTPMiddlewareForwardsFlush proves the middleware's response wrapper
// forwards Flush to the underlying http.Flusher (httptest.ResponseRecorder
// implements one, tracked via its own Flushed field) rather than silently
// downgrading a streaming/SSE handler wrapped behind it.
func TestHTTPMiddlewareForwardsFlush(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware())
	var sawFlusher bool
	r.Get("/stream", func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		sawFlusher = ok
		if ok {
			f.Flush()
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/stream", http.NoBody)
	r.ServeHTTP(rec, req)

	if !sawFlusher {
		t.Fatal("handler did not see an http.Flusher through the metrics middleware")
	}
	if !rec.Flushed {
		t.Fatal("Flush() was not forwarded to the underlying ResponseWriter")
	}
}

// hijackRecorder is an httptest.ResponseRecorder that also implements
// http.Hijacker, so TestHTTPMiddlewareForwardsHijack can prove the
// middleware's statusRecorder passes Hijack through.
type hijackRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

// TestHTTPMiddlewareForwardsHijack proves the middleware's response wrapper
// forwards Hijack to an underlying http.Hijacker rather than silently
// downgrading a protocol-upgrade endpoint wrapped behind it.
func TestHTTPMiddlewareForwardsHijack(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware())
	var sawHijacker bool
	r.Get("/upgrade", func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		sawHijacker = ok
		if ok {
			_, _, _ = hj.Hijack()
		}
	})

	hr := &hijackRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/upgrade", http.NoBody)
	r.ServeHTTP(hr, req)

	if !sawHijacker {
		t.Fatal("handler did not see an http.Hijacker through the metrics middleware")
	}
	if !hr.hijacked {
		t.Fatal("Hijack() was not forwarded to the underlying ResponseWriter")
	}
}

// TestHTTPMiddlewareHijackErrorsWhenUnsupported proves Hijack() returns an
// error (never panics) when the underlying ResponseWriter doesn't implement
// http.Hijacker — httptest.ResponseRecorder is exactly such a writer.
func TestHTTPMiddlewareHijackErrorsWhenUnsupported(t *testing.T) {
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(m.HTTPMiddleware())
	var hijackErr error
	r.Get("/upgrade", func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("statusRecorder must always implement http.Hijacker (even if it errors)")
		}
		_, _, hijackErr = hj.Hijack()
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/upgrade", http.NoBody)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if hijackErr == nil {
		t.Fatal("expected an error hijacking a non-Hijacker ResponseWriter, got nil")
	}
}
