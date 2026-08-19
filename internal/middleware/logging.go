// Package middleware holds cross-cutting HTTP middleware.
//
// Everything here uses the standard func(http.Handler) http.Handler shape, so
// it keeps working if the web framework behind internal/router changes.
package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// Logging records the method, path, status and duration of every request.
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			log.LogAttrs(r.Context(), slog.LevelInfo, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

// statusRecorder remembers the status code, which http.ResponseWriter does not
// expose once written. The default is 200 because a handler that writes a body
// without calling WriteHeader has implicitly sent one.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.NewResponseController reach the underlying writer, so
// wrapping does not cost handlers access to flushing or deadlines.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
