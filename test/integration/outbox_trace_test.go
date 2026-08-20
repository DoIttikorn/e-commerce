package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/DoIttikorn/e-commerce/internal/outbox"
)

// capturingPublisher records the context each publish was made with. The trace
// ID on that context is the whole subject of these tests.
type capturingPublisher struct {
	mu        sync.Mutex
	published []string // trace IDs, in publish order
	done      chan struct{}
	once      sync.Once
}

func newCapturingPublisher() *capturingPublisher {
	return &capturingPublisher{done: make(chan struct{})}
}

func (p *capturingPublisher) PublishRaw(ctx context.Context, _, _ string, _ []byte) error {
	p.mu.Lock()
	p.published = append(p.published, trace.SpanContextFromContext(ctx).TraceID().String())
	p.mu.Unlock()
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *capturingPublisher) traceIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.published...)
}

// drainOnce runs the relay until it has published something, or gives up.
func drainOnce(t *testing.T, ctx context.Context, relay *outbox.Relay, pub *capturingPublisher) {
	t.Helper()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		relay.Run(runCtx)
	}()

	select {
	case <-pub.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the relay published nothing within 10s")
	}
	cancel()
	<-finished
}

// The point of the whole exercise: an event written during a request is
// published, on a background loop, under the trace of the request that caused
// it. Without the stored context this is where every trace would end.
func TestTheOutboxCarriesTheTraceToThePublisher(t *testing.T) {
	db, ctx := mongoFor(t, "product")
	coll := db.Collection("outbox_trace_test")
	if err := coll.Drop(ctx); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := outbox.EnsureIndexes(ctx, coll); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}

	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Stand in for the span an HTTP request would have been running under.
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	requestCtx := trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true,
	}))

	err := outbox.Append(requestCtx, coll, []outbox.Event{{
		Topic:   "product.events",
		Key:     "product-1",
		Payload: json.RawMessage(`{"type":"product.created"}`),
	}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	// The request is over. Everything from here runs on a background loop with
	// a context that knows nothing about it.
	pub := newCapturingPublisher()
	drainOnce(t, ctx, outbox.NewRelay(coll, pub, discard()), pub)

	got := pub.traceIDs()
	if len(got) == 0 {
		t.Fatal("nothing was published")
	}
	if got[0] != traceID.String() {
		t.Errorf("published under trace %s, want the producing request's %s", got[0], traceID)
	}
}

// Tracing is optional. An event appended with no trace context still publishes.
func TestAnUntracedEventStillPublishes(t *testing.T) {
	db, ctx := mongoFor(t, "product")
	coll := db.Collection("outbox_untraced_test")
	if err := coll.Drop(ctx); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if err := outbox.EnsureIndexes(ctx, coll); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}

	err := outbox.Append(ctx, coll, []outbox.Event{{
		Topic:   "product.events",
		Key:     "product-2",
		Payload: json.RawMessage(`{"type":"product.created"}`),
	}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	pub := newCapturingPublisher()
	drainOnce(t, ctx, outbox.NewRelay(coll, pub, discard()), pub)

	if len(pub.traceIDs()) == 0 {
		t.Fatal("an event with no trace context was never published")
	}
}
