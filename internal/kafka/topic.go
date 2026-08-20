package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/segmentio/kafka-go"
)

// EnsureTopic creates topic if it does not already exist.
//
// Auto-creation on first publish is convenient but leaves a race: a consumer
// that subscribes before anything has been published has nothing to attach to
// and may sit idle past the point anyone is willing to wait. Creating the topic
// at startup removes the race, and gives the partition count somewhere
// deliberate to live rather than defaulting to whatever the broker prefers.
//
// It is idempotent: an existing topic is not an error.
func EnsureTopic(ctx context.Context, brokers []string, topic string, partitions int) error {
	if len(brokers) == 0 {
		return errors.New("no kafka brokers configured")
	}
	if partitions < 1 {
		partitions = 1
	}

	client := &kafka.Client{Addr: kafka.TCP(brokers...)}

	resp, err := client.CreateTopics(ctx, &kafka.CreateTopicsRequest{
		Topics: []kafka.TopicConfig{{
			Topic:         topic,
			NumPartitions: partitions,
			// Single-broker development default. A real cluster wants at least
			// three, and the value belongs in configuration when there is one.
			ReplicationFactor: 1,
		}},
	})
	if err != nil {
		return fmt.Errorf("create topic %s: %w", topic, err)
	}

	for name, topicErr := range resp.Errors {
		if topicErr == nil || errors.Is(topicErr, kafka.TopicAlreadyExists) {
			continue
		}
		return fmt.Errorf("create topic %s: %w", name, topicErr)
	}
	return nil
}
