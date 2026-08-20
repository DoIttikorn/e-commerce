package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/DoIttikorn/e-commerce/internal/middleware"
	"github.com/DoIttikorn/e-commerce/internal/router"
	"github.com/DoIttikorn/e-commerce/internal/router/chirouter"
)

// newTraced returns a router that records its spans in memory. No collector,
// no network: the SDK's own recorder is the exporter.
func newTraced(t *testing.T) (*tracetest.SpanRecorder, router.Router) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	r := chirouter.New()
	r.Use(middleware.Tracing("test-service"))
	return recorder, r
}

// The reason this middleware is hand-written instead of otelhttp: the span has
// to be named after the route, and the route is not known until the router has
// matched. A span name of "GET /api/v1/users/68f1..." is the trace-backend
// version of unbounded Prometheus label cardinality.
func TestSpanIsNamedByRoutePatternNotPath(t *testing.T) {
	recorder, r := newTraced(t)
	r.Handle(http.MethodGet, "/api/v1/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/68f1a2b3c4d5e6f708192a3b", nil)
	r.Handler().ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if got, want := spans[0].Name(), "GET /api/v1/users/{id}"; got != want {
		t.Errorf("span name = %q, want %q", got, want)
	}

	// The concrete path is still worth having — just as an attribute, where it
	// costs nothing, rather than as a name, where it costs a time series.
	if !hasAttribute(spans[0], "url.path", "/api/v1/users/68f1a2b3c4d5e6f708192a3b") {
		t.Error("url.path attribute missing the concrete path")
	}
}

// A request that matched no route must not mint a span name from whatever a
// scanner happened to probe.
func TestUnmatchedRequestsCollapseToOneSpanName(t *testing.T) {
	recorder, r := newTraced(t)
	r.Handle(http.MethodGet, "/healthz", func(w http.ResponseWriter, _ *http.Request) {})

	for _, path := range []string{"/wp-admin", "/.env", "/phpmyadmin"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	names := map[string]struct{}{}
	for _, span := range recorder.Ended() {
		names[span.Name()] = struct{}{}
	}
	if len(names) != 1 {
		t.Errorf("three unmatched paths produced %d span names (%v), want 1", len(names), names)
	}
}

// The whole point of the exercise: a request that arrives already traced joins
// that trace instead of starting its own.
func TestInboundTraceContextIsContinued(t *testing.T) {
	recorder, r := newTraced(t)
	r.Handle(http.MethodGet, "/healthz", func(w http.ResponseWriter, _ *http.Request) {})

	const upstreamTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("traceparent", "00-"+upstreamTrace+"-00f067aa0ba902b7-01")

	r.Handler().ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if got := spans[0].SpanContext().TraceID().String(); got != upstreamTrace {
		t.Errorf("trace ID = %q, want the caller's %q", got, upstreamTrace)
	}
	if spans[0].SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want server", spans[0].SpanKind())
	}
}

// A 404 is the server working. Marking it an error produces an error rate
// nobody can act on, which is how teams learn to ignore the error rate.
func TestOnlyServerErrorsMarkTheSpanFailed(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		wantError bool
	}{
		{"ok", http.StatusOK, false},
		{"not found", http.StatusNotFound, false},
		{"validation", http.StatusUnprocessableEntity, false},
		{"server error", http.StatusInternalServerError, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder, r := newTraced(t)
			r.Handle(http.MethodGet, "/x", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			})

			r.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

			spans := recorder.Ended()
			if len(spans) != 1 {
				t.Fatalf("recorded %d spans, want 1", len(spans))
			}
			gotError := spans[0].Status().Code == 1 /* codes.Error */
			if gotError != tt.wantError {
				t.Errorf("span error = %v for status %d, want %v", gotError, tt.status, tt.wantError)
			}
		})
	}
}

func hasAttribute(span sdktrace.ReadOnlySpan, key, value string) bool {
	for _, attr := range span.Attributes() {
		if string(attr.Key) == key && attr.Value.AsString() == value {
			return true
		}
	}
	return false
}

// The regression test for the stack appserver actually builds.
//
// Tracing and Metrics both want the matched route pattern, and both prepare the
// request to receive it. Before WithPatternCapture became idempotent the second
// call installed a second holder, the router filled that one, and the outer
// middleware — tracing — named every span "GET unmatched" while the metrics
// beneath it looked perfectly correct. Testing either alone proves nothing.
func TestTracingAndMetricsBothSeeTheRoutePattern(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	reg := prometheus.NewRegistry()
	r := chirouter.New()
	// The order appserver.New registers them in.
	r.Use(
		middleware.RequestID(),
		middleware.Tracing("test-service"),
		middleware.NewMetrics(reg).Middleware(),
	)
	r.Handle(http.MethodGet, "/api/v1/users/{id}", func(w http.ResponseWriter, _ *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/68f1a2b3c4d5e6f708192a3b", nil)
	r.Handler().ServeHTTP(httptest.NewRecorder(), req)

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if got, want := spans[0].Name(), "GET /api/v1/users/{id}"; got != want {
		t.Errorf("span name = %q, want %q", got, want)
	}

	// And the metrics label is still right, which is what makes this a
	// regression test rather than a swap of one bug for another.
	want := `
# HELP http_requests_total Total HTTP requests by route, method and status code.
# TYPE http_requests_total counter
http_requests_total{method="GET",route="/api/v1/users/{id}",status="200"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "http_requests_total"); err != nil {
		t.Error(err)
	}
}
