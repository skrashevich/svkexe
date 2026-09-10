package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Middleware returns a Chi-compatible middleware that records HTTP metrics.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Preserve Hijacker, Flusher and Unwrap for terminals, LLM streaming
		// and per-request write deadlines.
		rw := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(rw, r)

		// Use the Chi route pattern if available, falling back to the raw path.
		path := ""
		if route := chi.RouteContext(r.Context()); route != nil {
			path = route.RoutePattern()
		}
		if path == "" {
			path = r.URL.Path
		}

		status := rw.Status()
		if status == 0 {
			status = http.StatusOK
		}
		statusStr := strconv.Itoa(status)
		duration := time.Since(start).Seconds()

		HTTPRequestsTotal.WithLabelValues(r.Method, path, statusStr).Inc()
		HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}
