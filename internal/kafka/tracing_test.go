package kafka

import (
	"context"
	"testing"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// A message carries its producer's trace across the broker.
//
// This is the hop a request ID cannot make. The producing request has already
// returned by the time this message is read — possibly by a different process,
// minutes later — so the header is the only thing that connects the two.
func TestTraceContextSurvivesAMessage(t *testing.T) {
	propagator := propagation.TraceContext{}

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	producerCtx := trace.ContextWithSpanContext(context.Background(),
		trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true,
		}))

	msg := kafka.Message{Topic: "seller.events", Key: []byte("seller-1")}
	propagator.Inject(producerCtx, messageCarrier{msg: &msg})

	// Everything between the two lines is the broker: the message is bytes.
	consumerCtx := propagator.Extract(context.Background(), messageCarrier{msg: &msg})

	got := trace.SpanContextFromContext(consumerCtx)
	if !got.IsValid() {
		t.Fatal("the consumer got no trace context from the message headers")
	}
	if got.TraceID() != traceID {
		t.Errorf("trace ID = %s, want the producer's %s", got.TraceID(), traceID)
	}
	if !got.IsSampled() {
		t.Error("sampling decision was lost, so the trace would have a hole in it")
	}
}

// Kafka allows duplicate header keys. Two traceparents would leave the consumer
// to pick one, so Set replaces rather than appends.
func TestInjectingTwiceDoesNotDuplicateTheHeader(t *testing.T) {
	msg := kafka.Message{}
	carrier := messageCarrier{msg: &msg}

	carrier.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	carrier.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	count := 0
	for _, h := range msg.Headers {
		if h.Key == "traceparent" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("traceparent appears %d times, want 1", count)
	}
	if got := carrier.Get("traceparent"); got != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Errorf("traceparent = %q, want the second value", got)
	}
}

// An untraced message must be readable. Not every producer is instrumented,
// and a consumer that needs a header to work is a consumer that breaks the
// first time one is missing.
func TestAMessageWithNoHeadersIsNotAnError(t *testing.T) {
	msg := kafka.Message{Topic: "seller.events"}

	ctx := propagation.TraceContext{}.Extract(context.Background(), messageCarrier{msg: &msg})

	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("a message with no traceparent produced a valid span context")
	}
	if carrier := (messageCarrier{msg: &msg}); len(carrier.Keys()) != 0 {
		t.Errorf("Keys() = %v, want empty", carrier.Keys())
	}
}
