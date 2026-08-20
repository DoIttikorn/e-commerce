// Package tracing wires OpenTelemetry, and is the answer to the one question
// request IDs cannot answer once there is more than one service.
//
// A request ID correlates every log line *within* a process. It does not
// survive a gRPC call, and it certainly does not survive an event that is
// written to an outbox now and published to Kafka a second later. So "the
// checkout was slow" stays answerable only as far as the first hop: Order was
// slow, and something Order called was slow, and after that the trail is six
// separate log streams and a timestamp comparison.
//
// A trace is one ID propagated across all of it, with a span per unit of work
// hung off it. The propagation format is W3C Trace Context — a `traceparent`
// header on HTTP, gRPC metadata, a Kafka message header — which is a standard
// precisely so that a service written in another language still joins the same
// trace.
//
// Tracing is optional here on the same terms as Redis: unset OTEL_EXPORTER_
// OTLP_ENDPOINT and every call in this package becomes a cheap no-op rather
// than a startup failure. A binary that refuses to run without a collector is
// a binary that cannot be run locally.
package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// shutdownTimeout caps how long the exporter gets to flush on the way out.
// Spans are diagnostics: losing the last few is better than delaying a
// shutdown past the point an orchestrator will SIGKILL the process.
const shutdownTimeout = 5 * time.Second

// Config is what a service needs to export traces.
type Config struct {
	// Endpoint is the OTLP/gRPC collector address, host:port. Empty disables
	// tracing entirely.
	Endpoint string

	// ServiceName is what this process is called in the trace UI.
	ServiceName string

	// SampleRatio is the fraction of root traces recorded, 0..1.
	//
	// The decision is made once, at the root, and propagated — so a sampled
	// trace is sampled in every service it touches. Sampling per service
	// independently is the classic way to end up with traces full of holes.
	SampleRatio float64
}

// Init installs the global tracer provider and propagator, and returns the
// function that flushes it.
//
// The returned shutdown is never nil, so a caller can defer it without
// checking, and Init with no endpoint returns a no-op provider rather than an
// error — an unconfigured collector should not stop a service starting.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	// The propagator is installed either way. Without an exporter there is
	// nothing to send, but a service still has to pass through the trace
	// context it was given, or it becomes a hole in someone else's trace.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if cfg.Endpoint == "" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		// The collector link is inside the deployment. TLS on it is worth
		// having in production and is a configuration change, not a code one.
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return func(context.Context) error { return nil },
			fmt.Errorf("otlp exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", cfg.ServiceName),
	))
	if err != nil {
		return func(context.Context) error { return nil },
			fmt.Errorf("trace resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		// Batched, not synchronous: an exporter that blocks the request path
		// turns an observability outage into a service outage.
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// ParentBased is the important half. Sample the root at the configured
		// ratio, and downstream services honour whatever decision arrived with
		// the request rather than tossing their own coin.
		sdktrace.WithSampler(sdktrace.ParentBased(
			sdktrace.TraceIDRatioBased(clampRatio(cfg.SampleRatio)),
		)),
	)
	otel.SetTracerProvider(provider)

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()
		return provider.Shutdown(ctx)
	}, nil
}

// clampRatio keeps a mistyped ratio from silently disabling tracing or being
// rejected by the SDK.
func clampRatio(r float64) float64 {
	switch {
	case r <= 0:
		return 0
	case r >= 1:
		return 1
	default:
		return r
	}
}

// Tracer returns the tracer every package here should use. Named after the
// module so spans carry their origin, per the OpenTelemetry convention.
func Tracer() trace.Tracer {
	return otel.Tracer("github.com/DoIttikorn/e-commerce")
}

// TraceIDFrom returns the trace ID on ctx, or "" when there is none.
//
// It is what puts trace_id on a log line: given one, a log search and a trace
// view are the same investigation rather than two.
func TraceIDFrom(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}
