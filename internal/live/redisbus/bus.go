// Package redisbus is the driven adapter that makes Live Commerce work with
// more than one instance of the service running.
//
// Two problems, one store:
//
//   - Presence. A viewer count kept in process memory is per-instance, so with
//     two instances every viewer sees a number that is wrong by however many
//     people happen to be connected elsewhere.
//   - Broadcast. A WebSocket lives on exactly one instance. An event handled by
//     instance B has to reach a socket held by instance A, and no amount of
//     local bookkeeping can carry it across.
//
// Redis solves both: a sorted set that every instance can count, and a pub/sub
// channel that every instance can listen on.
package redisbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/DoIttikorn/e-commerce/internal/live"
)

const (
	presencePrefix = "live:viewers:"
	channelPrefix  = "live:stream:"

	// staleAfter is how long a viewer counts for without a heartbeat. It has to
	// be comfortably longer than the heartbeat interval, or a slow network
	// starts evicting people who are still watching.
	staleAfter = 90 * time.Second

	// keyTTL cleans up after a stream nobody watches any more, so an ended
	// broadcast does not leave a key behind for ever.
	keyTTL = 10 * time.Minute

	// subscribeBuffer is how many events a slow viewer may fall behind before
	// they start being dropped. A live feed is worth less the older it is, so
	// dropping is better than blocking every other viewer to wait for one.
	subscribeBuffer = 32
)

// Bus implements live.Presence and live.Broadcaster.
type Bus struct {
	rdb *redis.Client
	log *slog.Logger
}

var (
	_ live.Presence    = (*Bus)(nil)
	_ live.Broadcaster = (*Bus)(nil)
)

// New returns a Bus over rdb.
func New(rdb *redis.Client, log *slog.Logger) *Bus {
	return &Bus{rdb: rdb, log: log}
}

func presenceKey(streamID string) string { return presencePrefix + streamID }
func channelFor(streamID string) string  { return channelPrefix + streamID }

// Join records a viewer and returns the audience size.
//
// A sorted set scored by time, rather than a plain set, so that presence can
// expire. Adding the same viewer twice updates their score instead of
// double-counting, which is what makes a reconnect harmless.
func (b *Bus) Join(ctx context.Context, streamID, viewerID string) (int64, error) {
	key := presenceKey(streamID)
	now := float64(time.Now().Unix())

	pipe := b.rdb.TxPipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: now, Member: viewerID})
	pipe.Expire(ctx, key, keyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("join presence: %w", err)
	}
	return b.Count(ctx, streamID)
}

func (b *Bus) Leave(ctx context.Context, streamID, viewerID string) (int64, error) {
	if err := b.rdb.ZRem(ctx, presenceKey(streamID), viewerID).Err(); err != nil {
		return 0, fmt.Errorf("leave presence: %w", err)
	}
	return b.Count(ctx, streamID)
}

func (b *Bus) Heartbeat(ctx context.Context, streamID, viewerID string) error {
	key := presenceKey(streamID)

	pipe := b.rdb.TxPipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(time.Now().Unix()), Member: viewerID})
	pipe.Expire(ctx, key, keyTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}

// Count prunes the stale before counting.
//
// Pruning on read rather than on a timer means the cost falls on whoever is
// asking, and a stream nobody is watching costs nothing at all.
func (b *Bus) Count(ctx context.Context, streamID string) (int64, error) {
	key := presenceKey(streamID)
	cutoff := fmt.Sprintf("%d", time.Now().Add(-staleAfter).Unix())

	pipe := b.rdb.TxPipeline()
	pipe.ZRemRangeByScore(ctx, key, "-inf", "("+cutoff)
	count := pipe.ZCard(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("count viewers: %w", err)
	}
	return count.Val(), nil
}

// Publish fans an event out to every instance.
func (b *Bus) Publish(ctx context.Context, streamID string, e live.Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal live event: %w", err)
	}
	if err := b.rdb.Publish(ctx, channelFor(streamID), body).Err(); err != nil {
		return fmt.Errorf("publish live event: %w", err)
	}
	return nil
}

// Subscribe delivers a stream's events until ctx is cancelled.
//
// Redis pub/sub is fire-and-forget: a subscriber that is not listening when a
// message is published never sees it, and there is no replay. That is the right
// trade for a live feed — a purchase notification from thirty seconds ago is
// not worth showing — and the wrong one for anything that has to be durable,
// which is why orders go through Kafka and this does not.
func (b *Bus) Subscribe(ctx context.Context, streamID string) (<-chan live.Event, error) {
	sub := b.rdb.Subscribe(ctx, channelFor(streamID))

	// Confirm the subscription before returning, so a caller that publishes
	// immediately afterwards cannot beat its own subscriber into existence.
	if _, err := sub.Receive(ctx); err != nil {
		_ = sub.Close()
		return nil, fmt.Errorf("subscribe to %s: %w", streamID, err)
	}

	out := make(chan live.Event, subscribeBuffer)
	go func() {
		defer close(out)
		defer func() { _ = sub.Close() }()

		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-sub.Channel():
				if !ok {
					return
				}

				var event live.Event
				if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
					continue
				}

				select {
				case out <- event:
				default:
					// This viewer is not keeping up. Dropping their frame is
					// better than blocking the goroutine that serves everyone
					// else on the same connection pool.
					b.log.LogAttrs(ctx, slog.LevelWarn, "dropped a live event for a slow viewer",
						slog.String("stream_id", streamID))
				}
			}
		}
	}()

	return out, nil
}

// Ping exposes the bus's health for a readiness check.
func (b *Bus) Ping(ctx context.Context) error { return b.rdb.Ping(ctx).Err() }
