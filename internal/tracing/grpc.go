package tracing

import (
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
)

// gRPC needs no hand-written instrumentation, unlike HTTP.
//
// The reason the HTTP middleware is hand-written is span-name cardinality: a
// URL path contains IDs. A gRPC method does not — "/product.v1.StockService/
// Reserve" is the same string for every call — so the contrib handler names
// spans correctly with no help, and reimplementing it would buy nothing.
//
// Both are stats handlers rather than interceptors. An interceptor sees the
// call; a stats handler also sees the message sizes and the point the headers
// went out, which is the difference between "the RPC took 400ms" and "380ms of
// it was spent waiting for the first byte".

// ServerOption traces incoming RPCs and joins them to the caller's trace.
// It is safe with tracing disabled: the no-op provider makes it do nothing.
func ServerOption() grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler())
}

// DialOption traces outgoing RPCs and injects the trace context into the
// request metadata, which is how the trace survives the hop.
func DialOption() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler())
}
