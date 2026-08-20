package kafka

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"
)

// Handler processes one message. Returning an error leaves the offset
// uncommitted, so the message is delivered again.
type Handler func(ctx context.Context, key, value []byte) error

// Consumer reads one topic as part of a consumer group.
type Consumer struct {
	reader  *kafka.Reader
	log     *slog.Logger
	handler Handler
}

// NewConsumer returns a Consumer for topic within groupID.
//
// The group is what makes this horizontally scalable: add a second instance of
// the service and Kafka splits the partitions between them.
func NewConsumer(brokers []string, groupID, topic string, log *slog.Logger) *Consumer {
	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers: brokers,
			GroupID: groupID,
			Topic:   topic,

			// Start from the beginning when the group has no committed offset,
			// so a consumer added later still sees the history it needs to
			// build its own copy of the data.
			StartOffset: kafka.FirstOffset,

			MaxWait: time.Second,
		}),
		log: log,
	}
}

// Run reads until ctx is cancelled.
//
// FetchMessage and CommitMessages are used rather than ReadMessage because the
// commit must happen *after* the handler succeeds. That gives at-least-once
// delivery: a crash mid-handler replays the message rather than losing it,
// which is why handlers must be idempotent.
func (c *Consumer) Run(ctx context.Context) {
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			// Cancellation is the normal way out, not a failure.
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			c.log.LogAttrs(ctx, slog.LevelError, "kafka fetch failed",
				slog.String("topic", c.reader.Config().Topic),
				slog.String("error", err.Error()))
			continue
		}

		if err := c.handle(ctx, msg); err != nil {
			// Deliberately not committed: the message will be redelivered.
			// A production system would add a retry budget and a dead-letter
			// topic so one poison message cannot block the partition forever.
			continue
		}

		if err := c.reader.CommitMessages(ctx, msg); err != nil && ctx.Err() == nil {
			c.log.LogAttrs(ctx, slog.LevelError, "kafka commit failed",
				slog.String("error", err.Error()))
		}
	}
}

func (c *Consumer) handle(ctx context.Context, msg kafka.Message) error {
	if c.handler == nil {
		return nil
	}
	if err := c.handler(ctx, msg.Key, msg.Value); err != nil {
		c.log.LogAttrs(ctx, slog.LevelError, "event handling failed",
			slog.String("topic", msg.Topic),
			slog.String("key", string(msg.Key)),
			slog.String("error", err.Error()))
		return err
	}
	return nil
}

// Handle sets the handler. It must be called before Run.
func (c *Consumer) Handle(h Handler) { c.handler = h }

// Close releases the reader and leaves the consumer group cleanly.
func (c *Consumer) Close() error { return c.reader.Close() }
