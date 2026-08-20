// Package outbox is the transactional outbox: events written to the same
// database, in the same transaction, as the change that produced them.
//
// It exists because of a gap every service here otherwise has. A service that
// writes to its database and then publishes to Kafka has a window between the
// two: crash in it, or have the broker refuse, and the change is real while
// nobody was told. Returning an error does not help — the write has committed —
// so the event is simply lost, and the systems that needed it drift.
//
// Writing the event into the same transaction removes the window. What remains
// is publishing it afterwards, which is the relay's job and is allowed to be
// slow, retried, and duplicated. The trade is exactly the one worth making:
// at-least-once delivery instead of at-most-once.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// CollectionName is where pending events live.
const CollectionName = "outbox"

// Event is one event awaiting publication.
type Event struct {
	Topic     string
	Key       string
	Payload   json.RawMessage
	CreatedAt time.Time
}

type document struct {
	ID          bson.ObjectID `bson:"_id,omitempty"`
	Topic       string        `bson:"topic"`
	Key         string        `bson:"key"`
	PayloadJSON string        `bson:"payload_json"`
	CreatedAt   time.Time     `bson:"created_at"`
	PublishedAt *time.Time    `bson:"published_at"`
	ClaimedAt   *time.Time    `bson:"claimed_at"`
	Attempts    int           `bson:"attempts"`
}

// EnsureIndexes creates what the relay's query needs.
func EnsureIndexes(ctx context.Context, coll *mongo.Collection) error {
	_, err := coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// The relay's only query: unpublished, oldest first. A partial
			// index keeps it to the rows that are actually pending rather than
			// growing with everything ever sent.
			Keys: bson.D{{Key: "published_at", Value: 1}, {Key: "created_at", Value: 1}},
			Options: options.Index().SetName("pending").
				SetPartialFilterExpression(bson.M{"published_at": bson.M{"$eq": nil}}),
		},
		{
			// Published rows are kept briefly for debugging, then expire. An
			// outbox that grows forever is a table nobody reads and everybody
			// backs up.
			Keys:    bson.D{{Key: "published_at", Value: 1}},
			Options: options.Index().SetName("ttl_published").SetExpireAfterSeconds(24 * 60 * 60),
		},
	})
	if err != nil {
		return fmt.Errorf("create outbox indexes: %w", err)
	}
	return nil
}

// Append writes events inside whatever transaction ctx carries.
//
// It must be called from within one. Called outside, it still works and still
// records the events — it just stops being an outbox and becomes a second
// write that can fail on its own.
func Append(ctx context.Context, coll *mongo.Collection, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	docs := make([]any, 0, len(events))
	for _, e := range events {
		created := e.CreatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		docs = append(docs, document{
			Topic:       e.Topic,
			Key:         e.Key,
			PayloadJSON: string(e.Payload),
			CreatedAt:   created,
		})
	}

	if _, err := coll.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("append to outbox: %w", err)
	}
	return nil
}
