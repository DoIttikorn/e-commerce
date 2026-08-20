// Package mongodb is the driven adapter for the Marketplace domain: the
// projection itself, and the query that reads it.
package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/marketplace"
)

// Collection names, exported so integration tests can clean up after themselves.
const (
	CollectionName      = "listings"
	SalesCollectionName = "counted_sales"
)

// document is keyed by product ID rather than a generated one, so an event
// arriving twice updates the same row instead of adding a second.
type document struct {
	ProductID    string    `bson:"_id"`
	SellerID     string    `bson:"seller_id"`
	SellerName   string    `bson:"seller_name"`
	SellerActive bool      `bson:"seller_active"`
	Name         string    `bson:"name"`
	Description  string    `bson:"description"`
	PriceMinor   int64     `bson:"price_minor"`
	Currency     string    `bson:"currency"`
	InStock      bool      `bson:"in_stock"`
	SoldCount    int64     `bson:"sold_count"`
	UpdatedAt    time.Time `bson:"updated_at"`
}

func (d document) toDomain() marketplace.Listing {
	return marketplace.Listing{
		ProductID:    d.ProductID,
		SellerID:     d.SellerID,
		SellerName:   d.SellerName,
		SellerActive: d.SellerActive,
		Name:         d.Name,
		Description:  d.Description,
		PriceMinor:   d.PriceMinor,
		Currency:     d.Currency,
		InStock:      d.InStock,
		SoldCount:    d.SoldCount,
		UpdatedAt:    d.UpdatedAt,
	}
}

// Repository implements marketplace.Repository.
type Repository struct {
	db    *mongo.Database
	coll  *mongo.Collection
	sales *mongo.Collection
}

var _ marketplace.Repository = (*Repository)(nil)

// NewRepository returns a Repository over db.
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{
		db:    db,
		coll:  db.Collection(CollectionName),
		sales: db.Collection(SalesCollectionName),
	}
}

// EnsureIndexes creates what search needs.
//
// A read model earns its keep through its indexes: the same facts live in three
// other services, and the only reason to copy them here is to be able to ask
// questions of them quickly.
func (r *Repository) EnsureIndexes(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// One text index per collection is all MongoDB allows, so both
			// searchable fields go in it. Name is weighted above description
			// because a word in a title says more than the same word buried in
			// a paragraph.
			Keys: bson.D{{Key: "name", Value: "text"}, {Key: "description", Value: "text"}},
			Options: options.Index().SetName("search_text").
				SetWeights(bson.M{"name": 10, "description": 1}),
		},
		{
			// Browsing one shop, newest first: the common non-search path.
			Keys:    bson.D{{Key: "seller_id", Value: 1}, {Key: "updated_at", Value: -1}},
			Options: options.Index().SetName("seller_updated"),
		},
		{
			Keys:    bson.D{{Key: "price_minor", Value: 1}},
			Options: options.Index().SetName("price"),
		},
		{
			// Best-selling first. Descending, because nobody sorts ascending
			// by popularity.
			Keys:    bson.D{{Key: "sold_count", Value: -1}},
			Options: options.Index().SetName("sold"),
		},
		{
			Keys:    bson.D{{Key: "updated_at", Value: -1}},
			Options: options.Index().SetName("updated"),
		},
	})
	if err != nil {
		return fmt.Errorf("create listing indexes: %w", err)
	}
	return nil
}

func (r *Repository) UpsertListing(ctx context.Context, l marketplace.Listing) error {
	// seller_active is set on insert only. A seller event may well have arrived
	// before any of that shop's products did, and $setOnInsert stops a product
	// event from resurrecting a suspended shop.
	_, err := r.coll.UpdateByID(ctx, l.ProductID, bson.M{
		"$set": bson.M{
			"seller_id":   l.SellerID,
			"seller_name": l.SellerName,
			"name":        l.Name,
			"description": l.Description,
			"price_minor": l.PriceMinor,
			"currency":    l.Currency,
			"in_stock":    l.InStock,
			"updated_at":  l.UpdatedAt,
		},
		"$setOnInsert": bson.M{"sold_count": int64(0), "seller_active": true},
	}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert listing: %w", err)
	}
	return nil
}

func (r *Repository) RemoveListing(ctx context.Context, productID string) error {
	if _, err := r.coll.DeleteOne(ctx, bson.M{"_id": productID}); err != nil {
		return fmt.Errorf("remove listing: %w", err)
	}
	return nil
}

func (r *Repository) ApplySellerChange(ctx context.Context, sellerID, shopName string, active bool) (int64, error) {
	res, err := r.coll.UpdateMany(ctx,
		bson.M{"seller_id": sellerID},
		bson.M{"$set": bson.M{"seller_name": shopName, "seller_active": active}},
	)
	if err != nil {
		return 0, fmt.Errorf("apply seller change: %w", err)
	}
	return res.ModifiedCount, nil
}

// RecordSale counts an order once.
//
// The order ID is inserted as a claim in the same transaction as the
// increments. Without it, at-least-once delivery would inflate every
// popularity ranking a little more each time a consumer restarted.
func (r *Repository) RecordSale(ctx context.Context, orderID string, lines []marketplace.SoldLine) error {
	session, err := r.db.Client().StartSession()
	if err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		if _, err := r.sales.InsertOne(sc, bson.M{"_id": orderID, "counted_at": time.Now().UTC()}); err != nil {
			return nil, err
		}
		for _, line := range lines {
			if _, err := r.coll.UpdateByID(sc, line.ProductID,
				bson.M{"$inc": bson.M{"sold_count": int64(line.Quantity)}}); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})

	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Already counted. Not an error: this is the expected shape of a
			// redelivery, and treating it as a failure would have the consumer
			// retry forever.
			return nil
		}
		return fmt.Errorf("record sale: %w", err)
	}
	return nil
}
