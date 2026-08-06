// Package httpx holds HTTP server bootstrap, routing, and response helpers.
package httpx

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/inroad/inroad/internal/platform/metrics"
)

// NewRouter returns a chi mux pre-wired with standard middleware and a health
// check. mtx's HTTPMiddleware is mounted FIRST — outermost in the chain,
// before middleware.Recoverer — so it observes the true final status of every
// request, including a panic Recoverer converts to 500 (see HTTPMiddleware's
// doc comment). A nil mtx (metrics disabled) is a safe no-op, so callers
// never need their own nil check.
func NewRouter(logger *slog.Logger, mtx *metrics.Metrics) *chi.Mux {
	r := chi.NewRouter()
	r.Use(mtx.HTTPMiddleware())
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(limitRequestBody)
	r.Use(slogRequestLogger(logger))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return r
}

func slogRequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(started).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
