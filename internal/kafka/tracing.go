package kafka

import (
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel/propagation"
)

// messageCarrier adapts Kafka message headers to the OpenTelemetry propagator.
//
// It is what makes a trace survive the broker. The producing service injects
// the trace context into the headers, the message sits in a partition for as
// long as it sits there, and the consumer — possibly a different process, on a
// different machine, minutes later — extracts it and continues the same trace.
//
// Headers rather than the payload, deliberately: the payload is the domain's
// contract, and putting transport concerns in it means every consumer has to
// know about tracing to parse an event.
type messageCarrier struct{ msg *kafka.Message }

var _ propagation.TextMapCarrier = messageCarrier{}

func (c messageCarrier) Get(key string) string {
	for _, h := range c.msg.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// Set replaces rather than appends. Kafka headers permit duplicate keys, and a
// second traceparent would leave which one wins up to the consumer.
func (c messageCarrier) Set(key, value string) {
	for i, h := range c.msg.Headers {
		if h.Key == key {
			c.msg.Headers[i].Value = []byte(value)
			return
		}
	}
	c.msg.Headers = append(c.msg.Headers, kafka.Header{Key: key, Value: []byte(value)})
}

func (c messageCarrier) Keys() []string {
	keys := make([]string, 0, len(c.msg.Headers))
	for _, h := range c.msg.Headers {
		keys = append(keys, h.Key)
	}
	return keys
}
