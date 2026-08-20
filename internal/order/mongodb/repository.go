// Package mongodb is the driven adapter for the Order domain.
//
// It carries the transactional outbox: an order and the events it produced are
// written together or not at all, which is what closes the gap every other
// domain here still has — a write that succeeds and an event that never leaves.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/order"
	"github.com/DoIttikorn/e-commerce/internal/outbox"
)

// Collection names, exported so integration tests can clean up after themselves.
const (
	CollectionName = "orders"
	OutboxName     = outbox.CollectionName
)

type lineDoc struct {
	ProductID   string `bson:"product_id"`
	ProductName string `bson:"product_name"`
	UnitMinor   int64  `bson:"unit_minor"`
	Quantity    int    `bson:"quantity"`
}

type document struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	BuyerUserID    string        `bson:"buyer_user_id"`
	SellerID       string        `bson:"seller_id"`
	IdempotencyKey string        `bson:"idempotency_key"`
	Lines          []lineDoc     `bson:"lines"`
	TotalMinor     int64         `bson:"total_minor"`
	Currency       string        `bson:"currency"`
	Status         string        `bson:"status"`
	CreatedAt      time.Time     `bson:"created_at"`
	UpdatedAt      time.Time     `bson:"updated_at"`
}

func (d document) toDomain() order.Order {
	lines := make([]order.Line, 0, len(d.Lines))
	for _, l := range d.Lines {
		lines = append(lines, order.Line{
			ProductID:   l.ProductID,
			ProductName: l.ProductName,
			UnitMinor:   l.UnitMinor,
			Quantity:    l.Quantity,
		})
	}
	return order.Order{
		ID:             d.ID.Hex(),
		BuyerUserID:    d.BuyerUserID,
		SellerID:       d.SellerID,
		IdempotencyKey: d.IdempotencyKey,
		Lines:          lines,
		TotalMinor:     d.TotalMinor,
		Currency:       d.Currency,
		Status:         order.Status(d.Status),
		CreatedAt:      d.CreatedAt,
		UpdatedAt:      d.UpdatedAt,
	}
}

func toLineDocs(lines []order.Line) []lineDoc {
	out := make([]lineDoc, 0, len(lines))
	for _, l := range lines {
		out = append(out, lineDoc{
			ProductID:   l.ProductID,
			ProductName: l.ProductName,
			UnitMinor:   l.UnitMinor,
			Quantity:    l.Quantity,
		})
	}
	return out
}

// Repository implements order.Repository.
type Repository struct {
	db     *mongo.Database
	coll   *mongo.Collection
	outbox *mongo.Collection
}

var _ order.Repository = (*Repository)(nil)

// NewRepository returns a Repository over db.
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{
		db:     db,
		coll:   db.Collection(CollectionName),
		outbox: db.Collection(OutboxName),
	}
}

// NextID mints an ObjectID up front, so the event written beside the order can
// carry the order's ID rather than being patched after the fact.
func (r *Repository) NextID() string { return bson.NewObjectID().Hex() }

// EnsureIndexes creates what the domain depends on.
func (r *Repository) EnsureIndexes(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// The unique index is what makes idempotency true rather than
			// likely: two concurrent placements under one key cannot both
			// insert, whatever the application checked first.
			Keys:    bson.D{{Key: "idempotency_key", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uniq_idempotency_key"),
		},
		{
			Keys:    bson.D{{Key: "buyer_user_id", Value: 1}, {Key: "created_at", Value: -1}},
			Options: options.Index().SetName("buyer_created"),
		},
		{
			Keys:    bson.D{{Key: "seller_id", Value: 1}, {Key: "created_at", Value: -1}},
			Options: options.Index().SetName("seller_created"),
		},
	})
	if err != nil {
		return fmt.Errorf("create order indexes: %w", err)
	}
	return outbox.EnsureIndexes(ctx, r.outbox)
}

// Save writes the order and its events in one transaction.
func (r *Repository) Save(ctx context.Context, o order.Order, events []order.OutboxEvent) (order.Order, error) {
	session, err := r.db.Client().StartSession()
	if err != nil {
		return order.Order{}, fmt.Errorf("start session: %w", err)
	}
	defer session.EndSession(ctx)

	oid, err := bson.ObjectIDFromHex(o.ID)
	if err != nil {
		return order.Order{}, order.ErrInvalidID
	}

	out, err := session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		doc := document{
			ID:             oid,
			BuyerUserID:    o.BuyerUserID,
			SellerID:       o.SellerID,
			IdempotencyKey: o.IdempotencyKey,
			Lines:          toLineDocs(o.Lines),
			TotalMinor:     o.TotalMinor,
			Currency:       o.Currency,
			Status:         string(o.Status),
			CreatedAt:      o.CreatedAt,
			UpdatedAt:      o.UpdatedAt,
		}

		if _, err := r.coll.InsertOne(sc, doc); err != nil {
			return nil, err
		}
		if err := outbox.Append(sc, r.outbox, toOutboxEvents(events)); err != nil {
			return nil, err
		}
		return doc.toDomain(), nil
	})

	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// A concurrent placement under the same key won. Its order is the
			// answer to this request too.
			return r.ByIdempotencyKey(ctx, o.IdempotencyKey)
		}
		return order.Order{}, fmt.Errorf("save order: %w", err)
	}

	saved, _ := out.(order.Order)
	return saved, nil
}

func toOutboxEvents(events []order.OutboxEvent) []outbox.Event {
	out := make([]outbox.Event, 0, len(events))
	for _, e := range events {
		out = append(out, outbox.Event{
			Topic:     e.Topic,
			Key:       e.Key,
			Payload:   e.Payload,
			CreatedAt: e.CreatedAt,
		})
	}
	return out
}

func (r *Repository) ByID(ctx context.Context, id string) (order.Order, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return order.Order{}, order.ErrInvalidID
	}
	return r.findOne(ctx, bson.M{"_id": oid})
}

func (r *Repository) ByIdempotencyKey(ctx context.Context, key string) (order.Order, error) {
	return r.findOne(ctx, bson.M{"idempotency_key": key})
}

func (r *Repository) findOne(ctx context.Context, filter bson.M) (order.Order, error) {
	var doc document
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return order.Order{}, order.ErrOrderNotFound
		}
		return order.Order{}, fmt.Errorf("find order: %w", err)
	}
	return doc.toDomain(), nil
}

func (r *Repository) ListForBuyer(ctx context.Context, buyerUserID string, limit, offset int) ([]order.Order, int, error) {
	filter := bson.M{"buyer_user_id": buyerUserID}

	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("count orders: %w", err)
	}

	cursor, err := r.coll.Find(ctx, filter, options.Find().
		SetLimit(int64(limit)).
		SetSkip(int64(offset)).
		SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, 0, fmt.Errorf("list orders: %w", err)
	}

	var docs []document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, 0, fmt.Errorf("decode orders: %w", err)
	}

	orders := make([]order.Order, 0, len(docs))
	for _, d := range docs {
		orders = append(orders, d.toDomain())
	}
	return orders, int(total), nil
}

// UpdateStatus moves an order on, guarded on where it is now.
//
// The from clause is the concurrency control. Two cancels racing both read a
// pending order; only one matches {status: "pending"} when it writes, and the
// other is told the order has moved rather than silently cancelling twice.
func (r *Repository) UpdateStatus(
	ctx context.Context, id string, from, to order.Status, events []order.OutboxEvent,
) (order.Order, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return order.Order{}, order.ErrInvalidID
	}

	session, err := r.db.Client().StartSession()
	if err != nil {
		return order.Order{}, fmt.Errorf("start session: %w", err)
	}
	defer session.EndSession(ctx)

	out, err := session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		var doc document
		err := r.coll.FindOneAndUpdate(sc,
			bson.M{"_id": oid, "status": string(from)},
			bson.M{"$set": bson.M{"status": string(to), "updated_at": time.Now().UTC()}},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&doc)

		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, order.ErrNotPending
		}
		if err != nil {
			return nil, err
		}

		if err := outbox.Append(sc, r.outbox, toOutboxEvents(events)); err != nil {
			return nil, err
		}
		return doc.toDomain(), nil
	})

	if err != nil {
		if errors.Is(err, order.ErrNotPending) {
			return order.Order{}, err
		}
		return order.Order{}, fmt.Errorf("update order status: %w", err)
	}

	updated, _ := out.(order.Order)
	return updated, nil
}
