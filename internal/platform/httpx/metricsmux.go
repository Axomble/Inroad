package httpx

import (
	"net/http"
	"net/http/pprof"
)

// MetricsMux wraps metricsHandler (mtx.Handler()) for the dedicated,
// operator-only metrics listener (INROAD_METRICS_ADDR), optionally mounting
// net/http/pprof's /debug/pprof/* endpoints alongside it — the diagnostic
// path for a goroutine leak or heap growth on a remote worker.
//
// The pprof handlers are registered explicitly (pprof.Index/.Cmdline/...)
// rather than by importing net/http/pprof for its side effect of registering
// onto http.DefaultServeMux: this package builds its own mux instead of using
// the global default, and doing so keeps the endpoints gated behind
// enablePprof rather than live the instant the package is imported.
//
// When enablePprof is false, /debug/pprof/* falls through to metricsHandler
// like any other path — the same behavior as before this mux existed, so a
// deployment that never sets INROAD_PPROF_ENABLED sees no change.
//
// Callers MUST mount this only on the metrics listener, never the public API
// router: pprof exposes goroutine stacks, heap dumps and CPU profiles, which
// have no place behind end-user auth.
func MetricsMux(metricsHandler http.Handler, enablePprof bool) http.Handler {
	mux := http.NewServeMux()
	if enablePprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	mux.Handle("/", metricsHandler)
	return mux
}
