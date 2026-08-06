// Package metrics is Inroad's Prometheus instrumentation seam. One *Metrics
// holds a process's own registry and collectors — it is constructed once at a
// binary's composition root and injected everywhere it is consumed (CLAUDE.md
// bans package-level state-holding singletons), never registered onto
// prometheus.DefaultRegisterer. Every method is safe to call on a nil
// receiver (a no-op), so a caller with metrics disabled, or a unit test that
// never wires one up, needs no "metrics == nil" branch of its own.
package metrics

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// unmatchedRoute is the route label recorded for a request that never matched
// a registered chi pattern (a genuine 404, or a method chi rejected outright).
// Falling back to the raw URL path here would let a path-fuzzer, or any
// caller hitting real per-resource URLs, mint one Prometheus series per
// attempt — exactly the unbounded-cardinality problem the "label with the
// PATTERN, not the raw path" rule exists to prevent — so every unmatched
// request collapses onto this one fixed label instead.
const unmatchedRoute = "unmatched"

// Metrics holds one process's Prometheus registry and every collector it
// exposes.
type Metrics struct {
	registry     *prometheus.Registry
	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	sends        *prometheus.CounterVec
}

// New builds a Metrics with its own registry and pre-registered collectors.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inroad_http_requests_total",
			Help: "Total HTTP requests served, labeled by chi route pattern, method, and status code.",
		}, []string{"route", "method", "code"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "inroad_http_request_seconds",
			Help: "HTTP request duration in seconds, labeled by chi route pattern and method.",
		}, []string{"route", "method"}),
		sends: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inroad_sends_total",
			Help: `Total outbound sends finalized, labeled by kind ("campaign"|"warmup") and result ("sent"|"failed"|"skipped"|"deferred").`,
		}, []string{"kind", "result"}),
	}
	m.registry.MustRegister(m.httpRequests, m.httpDuration, m.sends)
	return m
}

// Handler serves this Metrics' registry in the Prometheus exposition format.
// Mount it ONLY on the dedicated metrics listener (INROAD_METRICS_ADDR) —
// never on the public API router, so exposing it raises no auth question. A
// nil receiver has no registry to serve, so it 404s rather than panicking;
// in practice this only fires if a caller mis-wires a disabled Metrics onto a
// live listener, since main.go only starts that listener with a real one.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.NotFoundHandler()
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// HTTPMiddleware records inroad_http_requests_total and
// inroad_http_request_seconds for every request it wraps. It must sit
// outermost in the platform middleware chain (before Recoverer) so that, by
// the time it reads the response back off its own recorder, that status
// reflects the true final outcome — including a panic Recoverer converted to
// 500, which would otherwise unwind straight past a middleware sitting INSIDE
// Recoverer without ever reaching its post-next.ServeHTTP code. The route
// label is read via chi.RouteContext(r.Context()).RoutePattern() AFTER
// next.ServeHTTP returns: chi still composes every r.Use middleware around
// its own routeHTTP tree walk regardless of registration order, so the
// pattern is already fully resolved (including through nested mounts) by
// then, whichever position in the chain this occupies.
//
// A nil receiver returns the identity middleware (a pure pass-through), so a
// process with metrics disabled — or a test exercising a handler without one
// — needs no nil check of its own.
func (m *Metrics) HTTPMiddleware() func(http.Handler) http.Handler {
	if m == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)

			route := unmatchedRoute
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					route = pattern
				}
			}
			m.httpRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
			m.httpDuration.WithLabelValues(route, r.Method).Observe(time.Since(started).Seconds())
		})
	}
}

// SendFinalized increments inroad_sends_total for one outbound send outcome:
// kind is "campaign" or "warmup"; result is "sent", "failed", "skipped", or
// "deferred". Called from the worker's finalize points (sequence.
// AdvanceHandler, warmup.SendHandler). A nil receiver — the zero value most
// unit tests get when they don't wire a Metrics into the handler under test —
// is a no-op.
func (m *Metrics) SendFinalized(kind, result string) {
	if m == nil {
		return
	}
	m.sends.WithLabelValues(kind, result).Inc()
}

// statusRecorder captures the status code the wrapped handler actually wrote,
// defaulting to 200 like http.ResponseWriter itself does for a handler that
// never calls WriteHeader explicitly. Embedding http.ResponseWriter already
// forwards every OTHER method the concrete writer might have — except the
// two optional capability interfaces net/http callers commonly type-assert
// for (http.Flusher for streaming/SSE responses, http.Hijacker for
// WebSocket-style protocol upgrades), which Go does NOT forward through an
// embedded interface automatically: a type assertion on *statusRecorder
// itself only succeeds if *statusRecorder declares the method. Without the
// explicit passthroughs below, wrapping ANY handler in this middleware would
// silently downgrade it to "no Flush, no Hijack" the moment
// http.ResponseWriter is embedded rather than the concrete writer used
// directly — a future SSE or upgrade endpoint would misbehave with no
// compile-time signal.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (rec *statusRecorder) WriteHeader(status int) {
	if rec.wroteHeader {
		return
	}
	rec.wroteHeader = true
	rec.status = status
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *statusRecorder) Write(p []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	return rec.ResponseWriter.Write(p)
}

// Flush forwards to the underlying writer's http.Flusher when it implements
// one (e.g. for a streaming/SSE response); otherwise it is a silent no-op,
// matching how http.Flusher already documents "the caller may ignore
// whether Flush is a no-op".
func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwards to the underlying writer's http.Hijacker when it
// implements one, so a protocol-upgrade endpoint mounted behind this
// middleware keeps working. Returns an error (never panics) when the
// underlying writer doesn't support it, matching http.Hijacker's own
// documented contract.
func (rec *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := rec.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("metrics: underlying ResponseWriter (%T) does not support http.Hijacker", rec.ResponseWriter)
	}
	return hj.Hijack()
}
