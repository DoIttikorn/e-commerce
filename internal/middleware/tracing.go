package middleware

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/DoIttikorn/e-commerce/internal/router"
	"github.com/DoIttikorn/e-commerce/internal/tracing"
)

// Tracing starts a server span for every request and joins it to the caller's
// trace when one arrives in the headers.
//
// This is hand-written rather than otelhttp, for the same reason the metrics
// middleware labels by route pattern: otelhttp names the span when the span
// starts, which is before chi has matched a route, so the only thing available
// to name it with is r.URL.Path. That gives /api/v1/users/68f1… its own span
// name per user, and a trace backend groups, indexes, and bills by span name
// exactly as Prometheus does by label. The fix is the same one the project
// already uses — capture the pattern, then rename the span on the way out,
// which the OpenTelemetry API explicitly allows before End.
//
// Register it after RequestID so both IDs land on the same log lines.
func Tracing(serviceName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract first. A traceparent header means this request is already
			// part of somebody's trace, and starting a fresh root here would
			// break the chain at exactly the boundary it exists to cross.
			// Read per request, not captured at construction: the global
			// propagator is installed by tracing.Init, and capturing it here
			// would silently bind whichever middleware was built first to the
			// no-op default.
			ctx := otel.GetTextMapPropagator().
				Extract(r.Context(), propagation.HeaderCarrier(r.Header))

			ctx, span := tracing.Tracer().Start(ctx, r.Method,
				trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(
					attribute.String("http.request.method", r.Method),
					attribute.String("url.path", r.URL.Path),
					attribute.String("server.address", r.Host),
					attribute.String("service.name", serviceName),
				),
			)
			defer span.End()

			r = router.WithPatternCapture(r.WithContext(ctx))
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			// Renaming after the fact is the whole trick: by now the router has
			// matched, so the name is "GET /api/v1/users/{id}" — one span name
			// per endpoint, not one per user.
			route := router.PatternFrom(r)
			if route == "" {
				route = unmatchedRoute
			}
			span.SetName(r.Method + " " + route)
			span.SetAttributes(
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", rec.status),
			)

			// Only 5xx marks the span as an error. A 404 or a 422 is the server
			// working correctly; marking those red makes an error rate that
			// nobody can act on.
			if rec.status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(rec.status))
			}
		})
	}
}
