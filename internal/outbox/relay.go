package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Defaults for the relay loop.
const (
	// DefaultInterval is how long to wait after finding nothing. Finding
	// something loops again immediately, so a burst drains at full speed and an
	// idle outbox costs one query a second.
	DefaultInterval = time.Second

	// DefaultLease is how long a claimed row stays claimed. Too short and two
	// relays publish the same event; too long and a crashed relay's work sits
	// idle. Either way the consumer must be idempotent, which it has to be
	// regardless.
	DefaultLease = 30 * time.Second
)

// RawPublisher writes an already-encoded payload.
//
// Raw rather than a value: the payload was serialised inside the transaction
// that produced it, and re-encoding it here would let a change to the event
// type silently alter events written before that change.
type RawPublisher interface {
	PublishRaw(ctx context.Context, topic, key string, body []byte) error
}

// Relay moves events from the outbox to the broker.
type Relay struct {
	coll     *mongo.Collection
	pub      RawPublisher
	log      *slog.Logger
	interval time.Duration
	lease    time.Duration
}

// NewRelay returns a Relay over coll.
func NewRelay(coll *mongo.Collection, pub RawPublisher, log *slog.Logger) *Relay {
	return &Relay{
		coll:     coll,
		pub:      pub,
		log:      log,
		interval: DefaultInterval,
		lease:    DefaultLease,
	}
}

// Run drains the outbox until ctx is cancelled.
//
// Delivery is at-least-once by construction: the event is published before it
// is marked sent, so a crash in between republishes it. Consumers have to cope
// with a repeat anyway — Kafka gives them the same guarantee — so this adds no
// requirement they did not already have.
func (r *Relay) Run(ctx context.Context) {
	for {
		published, err := r.drainOne(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.log.LogAttrs(ctx, slog.LevelError, "outbox relay failed",
				slog.String("error", err.Error()))
		}

		if published {
			// More may be waiting; do not sleep through a backlog.
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(r.interval):
		}
	}
}

func (r *Relay) drainOne(ctx context.Context) (bool, error) {
	claimed, err := r.claim(ctx)
	if err != nil || claimed == nil {
		return false, err
	}

	if err := r.pub.PublishRaw(ctx, claimed.Topic, claimed.Key, []byte(claimed.PayloadJSON)); err != nil {
		// Left claimed. The lease expires and it is tried again, which is the
		// whole reason a broker being down is survivable rather than lossy.
		return false, err
	}

	now := time.Now().UTC()
	_, err = r.coll.UpdateByID(ctx, claimed.ID, bson.M{"$set": bson.M{"published_at": now}})
	if err != nil {
		// Published but not marked: it will be sent again after the lease.
		// That is the at-least-once edge, and it is why keys matter.
		return true, err
	}
	return true, nil
}

// claim takes the oldest pending event, or one whose lease has expired.
func (r *Relay) claim(ctx context.Context) (*document, error) {
	cutoff := time.Now().UTC().Add(-r.lease)
	now := time.Now().UTC()

	var doc document
	err := r.coll.FindOneAndUpdate(ctx,
		bson.M{
			"published_at": nil,
			"$or": []bson.M{
				{"claimed_at": nil},
				{"claimed_at": bson.M{"$lt": cutoff}},
			},
		},
		bson.M{"$set": bson.M{"claimed_at": now}, "$inc": bson.M{"attempts": 1}},
		options.FindOneAndUpdate().
			// Oldest first, so events leave in the order they were produced.
			SetSort(bson.D{{Key: "created_at", Value: 1}}).
			SetReturnDocument(options.After),
	).Decode(&doc)

	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

// PendingCount reports how many events are waiting. It is the number to alert
// on: a relay that has stopped looks exactly like an idle one until this climbs.
func PendingCount(ctx context.Context, coll *mongo.Collection) (int64, error) {
	return coll.CountDocuments(ctx, bson.M{"published_at": nil})
}
