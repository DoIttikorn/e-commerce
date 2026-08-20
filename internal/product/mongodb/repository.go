// Package mongodb is the driven adapter for the Product domain.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/outbox"
	"github.com/DoIttikorn/e-commerce/internal/product"
)

// CollectionName is exported so integration tests can clean up after themselves.
const CollectionName = "products"

type document struct {
	ID          bson.ObjectID `bson:"_id,omitempty"`
	SellerID    string        `bson:"seller_id"`
	SellerName  string        `bson:"seller_name"`
	Name        string        `bson:"name"`
	Description string        `bson:"description"`
	PriceMinor  int64         `bson:"price_minor"`
	Currency    string        `bson:"currency"`
	Stock       int           `bson:"stock"`
	CreatedAt   time.Time     `bson:"created_at"`
}

func (d document) toDomain() product.Product {
	return product.Product{
		ID:          d.ID.Hex(),
		SellerID:    d.SellerID,
		SellerName:  d.SellerName,
		Name:        d.Name,
		Description: d.Description,
		PriceMinor:  d.PriceMinor,
		Currency:    d.Currency,
		Stock:       d.Stock,
		CreatedAt:   d.CreatedAt,
	}
}

// OutboxName is where this domain's pending events live.
const OutboxName = outbox.CollectionName

// Repository implements product.Repository.
type Repository struct {
	db      *mongo.Database
	coll    *mongo.Collection
	outboxC *mongo.Collection
}

var _ product.Repository = (*Repository)(nil)

// NewRepository returns a Repository over db's products collection.
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{
		db:      db,
		coll:    db.Collection(CollectionName),
		outboxC: db.Collection(OutboxName),
	}
}

// NextID mints an ObjectID up front, so the event written beside a product can
// carry its ID rather than being patched after the fact.
func (r *Repository) NextID() string { return bson.NewObjectID().Hex() }

// inTransaction runs fn inside a session transaction.
func (r *Repository) inTransaction(ctx context.Context, fn func(context.Context) (any, error)) (any, error) {
	session, err := r.db.Client().StartSession()
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}
	defer session.EndSession(ctx)

	return session.WithTransaction(ctx, fn)
}

func toOutboxEvents(events []product.OutboxEvent) []outbox.Event {
	out := make([]outbox.Event, 0, len(events))
	for _, e := range events {
		out = append(out, outbox.Event{
			Topic: e.Topic, Key: e.Key, Payload: e.Payload, CreatedAt: e.CreatedAt,
		})
	}
	return out
}

// EnsureIndexes creates the indexes the query patterns need.
//
// Neither is unique: a seller may list two products with the same name, and
// nothing here is a constraint. They exist so that listing a seller's catalogue
// does not become a collection scan the day the catalogue is large.
func (r *Repository) EnsureIndexes(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// Compound and ordered to match the List sort, so MongoDB can walk
			// the index instead of sorting the result in memory.
			Keys:    bson.D{{Key: "seller_id", Value: 1}, {Key: "created_at", Value: -1}},
			Options: options.Index().SetName("seller_created"),
		},
		{
			Keys:    bson.D{{Key: "created_at", Value: -1}},
			Options: options.Index().SetName("created"),
		},
	})
	if err != nil {
		return fmt.Errorf("create product indexes: %w", err)
	}

	// Reservations are keyed by the caller's idempotency key, so _id is already
	// unique. This index expires them: a reservation record is only needed for
	// as long as a retry might arrive, and keeping them forever grows a
	// collection nobody reads.
	_, err = r.reservations().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().SetName("ttl_created").SetExpireAfterSeconds(7 * 24 * 60 * 60),
	})
	if err != nil {
		return fmt.Errorf("create reservation index: %w", err)
	}
	return outbox.EnsureIndexes(ctx, r.outboxC)
}

// Create writes the product and its events in one transaction.
func (r *Repository) Create(ctx context.Context, p product.Product, events []product.OutboxEvent) (product.Product, error) {
	oid, err := bson.ObjectIDFromHex(p.ID)
	if err != nil {
		return product.Product{}, product.ErrInvalidID
	}

	doc := document{
		ID:          oid,
		SellerID:    p.SellerID,
		SellerName:  p.SellerName,
		Name:        p.Name,
		Description: p.Description,
		PriceMinor:  p.PriceMinor,
		Currency:    p.Currency,
		Stock:       p.Stock,
		CreatedAt:   p.CreatedAt,
	}

	out, err := r.inTransaction(ctx, func(sc context.Context) (any, error) {
		if _, err := r.coll.InsertOne(sc, doc); err != nil {
			return nil, err
		}
		if err := outbox.Append(sc, r.outboxC, toOutboxEvents(events)); err != nil {
			return nil, err
		}
		return doc.toDomain(), nil
	})
	if err != nil {
		return product.Product{}, fmt.Errorf("insert product: %w", err)
	}

	created, _ := out.(product.Product)
	return created, nil
}

func (r *Repository) ByID(ctx context.Context, id string) (product.Product, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return product.Product{}, product.ErrInvalidID
	}

	var doc document
	if err := r.coll.FindOne(ctx, bson.M{"_id": oid}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return product.Product{}, product.ErrProductNotFound
		}
		return product.Product{}, fmt.Errorf("find product: %w", err)
	}
	return doc.toDomain(), nil
}

func (r *Repository) List(ctx context.Context, sellerID string, limit, offset int) ([]product.Product, int, error) {
	filter := bson.M{}
	if sellerID != "" {
		filter["seller_id"] = sellerID
	}

	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("count products: %w", err)
	}

	cursor, err := r.coll.Find(ctx, filter, options.Find().
		SetLimit(int64(limit)).
		SetSkip(int64(offset)).
		SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, 0, fmt.Errorf("list products: %w", err)
	}

	var docs []document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, 0, fmt.Errorf("decode products: %w", err)
	}

	products := make([]product.Product, 0, len(docs))
	for _, d := range docs {
		products = append(products, d.toDomain())
	}
	return products, int(total), nil
}

// Update writes the change and its events in one transaction.
func (r *Repository) Update(ctx context.Context, id string, upd product.Update, events []product.OutboxEvent) (product.Product, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return product.Product{}, product.ErrInvalidID
	}

	set := bson.M{}
	if upd.Name != nil {
		set["name"] = *upd.Name
	}
	if upd.Description != nil {
		set["description"] = *upd.Description
	}
	if upd.PriceMinor != nil {
		set["price_minor"] = *upd.PriceMinor
	}
	if upd.Stock != nil {
		set["stock"] = *upd.Stock
	}
	if len(set) == 0 {
		return r.ByID(ctx, id)
	}

	out, err := r.inTransaction(ctx, func(sc context.Context) (any, error) {
		var doc document
		if err := r.coll.FindOneAndUpdate(sc,
			bson.M{"_id": oid},
			bson.M{"$set": set},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&doc); err != nil {
			return nil, err
		}
		if err := outbox.Append(sc, r.outboxC, toOutboxEvents(events)); err != nil {
			return nil, err
		}
		return doc.toDomain(), nil
	})

	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		return product.Product{}, product.ErrProductNotFound
	case err != nil:
		return product.Product{}, fmt.Errorf("update product: %w", err)
	}

	updated, _ := out.(product.Product)
	return updated, nil
}

// Delete removes the product and records its delisting in one transaction.
func (r *Repository) Delete(ctx context.Context, id string, events []product.OutboxEvent) error {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return product.ErrInvalidID
	}

	_, err = r.inTransaction(ctx, func(sc context.Context) (any, error) {
		res, err := r.coll.DeleteOne(sc, bson.M{"_id": oid})
		if err != nil {
			return nil, err
		}
		if res.DeletedCount == 0 {
			return nil, product.ErrProductNotFound
		}
		return nil, outbox.Append(sc, r.outboxC, toOutboxEvents(events))
	})

	if errors.Is(err, product.ErrProductNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("delete product: %w", err)
	}
	return nil
}

// RenameSeller applies a new shop name across a seller's catalogue.
//
// The matching IDs are collected before the write so the caller can invalidate
// exactly those cache entries. The filter excludes products that already carry
// the new name, which makes a repeated event — and at-least-once delivery
// guarantees repeats — cost one indexed query and no writes at all.
func (r *Repository) RenameSeller(ctx context.Context, sellerID, shopName string) ([]string, error) {
	filter := bson.M{"seller_id": sellerID, "seller_name": bson.M{"$ne": shopName}}

	cursor, err := r.coll.Find(ctx, filter, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, fmt.Errorf("find products to rename: %w", err)
	}

	var ids []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cursor.All(ctx, &ids); err != nil {
		return nil, fmt.Errorf("decode product ids: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	if _, err := r.coll.UpdateMany(ctx, filter, bson.M{"$set": bson.M{"seller_name": shopName}}); err != nil {
		return nil, fmt.Errorf("rename seller on products: %w", err)
	}

	affected := make([]string, 0, len(ids))
	for _, item := range ids {
		affected = append(affected, item.ID.Hex())
	}
	return affected, nil
}
