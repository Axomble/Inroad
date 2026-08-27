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
	"github.com/prometheus/client_golang/prometheus/collectors"
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

// The outcome label values SendClaimed accepts. Exported as constants (rather
// than left as string literals at the call site) so the emitting package and
// any test asserting on the series agree on the spelling by compilation rather
// than by luck — a typo'd label silently mints a parallel series that no
// dashboard queries.
const (
	// ClaimOutcomeWon: a fresh row was inserted and this worker owns the send.
	ClaimOutcomeWon = "won"
	// ClaimOutcomeReclaimed: an expired lease was taken over from a worker that
	// died mid-send. The rate of this is the send path's health signal.
	ClaimOutcomeReclaimed = "reclaimed"
	// ClaimOutcomeAlreadySent: the row was already delivered; the caller
	// recovers forward (advances the cursor) without re-sending.
	ClaimOutcomeAlreadySent = "already_sent"
	// ClaimOutcomeDeferred: not due yet, or the mailbox's send interval has not
	// elapsed. Normal backpressure, not a failure.
	ClaimOutcomeDeferred = "deferred"
	// ClaimOutcomeLost: another worker holds a live lease, or the row is
	// terminal. A sustained rate means two drivers are racing the same step.
	ClaimOutcomeLost = "lost"
)

// Metrics holds one process's Prometheus registry and every collector it
// exposes.
type Metrics struct {
	registry      *prometheus.Registry
	httpRequests  *prometheus.CounterVec
	httpDuration  *prometheus.HistogramVec
	sends         *prometheus.CounterVec
	claims        *prometheus.CounterVec
	sweepDuration *prometheus.HistogramVec
	sweepRows     *prometheus.CounterVec
}

// sweepRowBuckets bound the rows-scanned histogram-free counter's companion
// duration histogram. A sweep is a periodic reconcile (5-minute cadence at the
// fastest), so the interesting range is "sub-second" through "longer than the
// interval, and therefore overlapping itself" — the default Prometheus buckets
// top out at 10s, which would collapse every genuinely pathological sweep into
// one +Inf bucket and hide exactly the growth this metric exists to show.
var sweepDurationBuckets = []float64{0.05, 0.25, 1, 5, 15, 60, 300, 900}

// New builds a Metrics with its own registry and pre-registered collectors.
// The Go runtime and process collectors are included unconditionally: they
// cost nothing to register, need no wiring from the caller, and are what
// answers "is this a Go-side problem (GC, goroutine leak, fd exhaustion) or a
// downstream one" before any Inroad-specific metric is consulted.
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
		claims: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inroad_send_claims_total",
			Help: `Claim-before-send attempts, labeled by kind ("step") and outcome ("won"|"reclaimed"|"already_sent"|"deferred"|"lost"). A rising "reclaimed" rate means workers are dying mid-send; a rising "lost" rate means duplicate drive of the same step.`,
		}, []string{"kind", "outcome"}),
		sweepDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "inroad_sweep_seconds",
			Help:    "Reconcile-sweep wall time in seconds, labeled by sweep kind.",
			Buckets: sweepDurationBuckets,
		}, []string{"kind"}),
		sweepRows: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "inroad_sweep_rows_total",
			Help: "Cumulative rows a reconcile sweep scanned, labeled by sweep kind. Divided by inroad_sweep_seconds' count this is rows-per-sweep — the growth curve of the known-unbounded inbox/warmup/enrollment scans.",
		}, []string{"kind"}),
	}
	m.registry.MustRegister(
		m.httpRequests, m.httpDuration, m.sends, m.claims, m.sweepDuration, m.sweepRows,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
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

// SendClaimed increments inroad_send_claims_total for one claim-before-send
// attempt: kind is the claim path ("step"), outcome is "won", "reclaimed",
// "already_sent", "deferred" or "lost". Emitted from the coreapi claim itself,
// where the outcome is already branched on, so every claim — including the
// ones a worker never learns the reason for — is counted exactly once.
//
// The lost-vs-reclaimed split is the point: "lost" means two drivers raced the
// same step (sweeper vs. lazy chain), while "reclaimed" means a previous
// worker died holding the lease. They are the two distinct send-path health
// signals and a single "not won" counter would conflate them.
//
// No workspace label (spec §7): the tenant a claim belonged to is in the
// structured log line and the sends row, not in a Prometheus dimension.
//
// A nil receiver is a no-op.
func (m *Metrics) SendClaimed(kind, outcome string) {
	if m == nil {
		return
	}
	m.claims.WithLabelValues(kind, outcome).Inc()
}

// SweepCompleted records one reconcile sweep's wall time and the number of
// rows it scanned, labeled by sweep kind ("enrollments", "inbox", "warmup",
// …). Call it once per sweep, after the scan, with the candidate-row count the
// handler already logs.
//
// This is measurement only, deliberately: the inbox and warmup sweeps scan
// unbounded result sets, and the whole reason to graph rows-per-sweep is to
// see that growth coming rather than to paper over it here.
//
// A nil receiver is a no-op.
func (m *Metrics) SweepCompleted(kind string, rows int, elapsed time.Duration) {
	if m == nil {
		return
	}
	m.sweepDuration.WithLabelValues(kind).Observe(elapsed.Seconds())
	// A negative count would corrupt a counter permanently (Prometheus counters
	// only go up); clamping is safer than trusting an arithmetic slip upstream.
	if rows > 0 {
		m.sweepRows.WithLabelValues(kind).Add(float64(rows))
	}
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
