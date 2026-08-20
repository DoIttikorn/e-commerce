// Package kafka wraps the Kafka client so domains publish and consume events
// without depending on the driver.
//
// Events exist here for one reason: when something happens in one domain and
// more than one other domain needs to know, the alternative is the publisher
// calling each of them in turn — which couples it to every consumer, fails
// when any one of them is down, and grows a new call with every new consumer.
package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
)

// Publisher writes events to Kafka.
type Publisher struct {
	writer *kafka.Writer
	log    *slog.Logger
}

// NewPublisher returns a Publisher for the given brokers.
func NewPublisher(brokers []string, log *slog.Logger) *Publisher {
	return &Publisher{
		writer: &kafka.Writer{
			Addr: kafka.TCP(brokers...),

			// Hash routes by key, so every event about one entity lands on the
			// same partition and is therefore delivered in order. Without it a
			// rename followed by a second rename can be applied backwards.
			Balancer: &kafka.Hash{},

			// Wait for all in-sync replicas: an event that is lost is worse
			// than a request that is slow, because nothing will retry it.
			RequiredAcks: kafka.RequireAll,

			// Convenient for local development. In a real deployment topics are
			// created deliberately, with a partition count chosen for the load.
			AllowAutoTopicCreation: true,

			WriteTimeout: 10 * time.Second,
		},
		log: log,
	}
}

// Publish marshals payload as JSON and writes it to topic under key.
//
// Ordering is per key, so the key should identify the entity the event is
// about — the seller ID for a seller event, not a random UUID.
func (p *Publisher) Publish(ctx context.Context, topic, key string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return p.PublishRaw(ctx, topic, key, body)
}

// PublishRaw writes an already-encoded payload.
//
// The outbox relay uses this: the payload was serialised inside the transaction
// that produced it, and re-encoding it here would let a later change to the
// event type quietly rewrite events recorded before that change.
func (p *Publisher) PublishRaw(ctx context.Context, topic, key string, body []byte) error {
	err := p.writer.WriteMessages(ctx, kafka.Message{
		Topic: topic,
		Key:   []byte(key),
		Value: body,
	})
	if err != nil {
		return fmt.Errorf("publish to %s: %w", topic, err)
	}

	p.log.LogAttrs(ctx, slog.LevelDebug, "event published",
		slog.String("topic", topic), slog.String("key", key))
	return nil
}

// Close flushes and releases the writer.
func (p *Publisher) Close() error { return p.writer.Close() }
