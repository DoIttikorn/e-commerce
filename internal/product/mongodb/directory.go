package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/product"
)

// DirectoryCollectionName holds Product's own copy of the seller facts it needs.
//
// It is a read model, not a source of truth: every row in it was put there by
// an event. Product never queries the Seller service and never reads its
// collections — which is what lets the two be deployed, scaled and failed
// independently.
const DirectoryCollectionName = "seller_directory"

type directoryDocument struct {
	SellerID string `bson:"seller_id"`
	UserID   string `bson:"user_id"`
	ShopName string `bson:"shop_name"`
	Status   string `bson:"status"`
}

func (d directoryDocument) toDomain() product.SellerRef {
	return product.SellerRef{
		SellerID: d.SellerID,
		UserID:   d.UserID,
		ShopName: d.ShopName,
		Status:   d.Status,
	}
}

// Directory implements product.SellerDirectory.
type Directory struct {
	coll *mongo.Collection
}

var _ product.SellerDirectory = (*Directory)(nil)

// NewDirectory returns a Directory over db's seller directory collection.
func NewDirectory(db *mongo.Database) *Directory {
	return &Directory{coll: db.Collection(DirectoryCollectionName)}
}

// EnsureIndexes makes seller_id unique, which is what makes Upsert safe to
// call concurrently for the same seller, and indexes user_id for the
// owner lookup on the product write path.
func (d *Directory) EnsureIndexes(ctx context.Context) error {
	_, err := d.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "seller_id", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uniq_seller_id"),
		},
		{
			Keys:    bson.D{{Key: "user_id", Value: 1}},
			Options: options.Index().SetName("owner"),
		},
	})
	if err != nil {
		return fmt.Errorf("create seller directory indexes: %w", err)
	}
	return nil
}

// Upsert records what an event said. It is idempotent by construction: an
// upsert of the same values twice leaves the same row, which is exactly what
// at-least-once delivery requires of a handler.
func (d *Directory) Upsert(ctx context.Context, ref product.SellerRef) error {
	_, err := d.coll.UpdateOne(ctx,
		bson.M{"seller_id": ref.SellerID},
		bson.M{"$set": bson.M{
			"user_id":   ref.UserID,
			"shop_name": ref.ShopName,
			"status":    ref.Status,
		}},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("upsert seller directory: %w", err)
	}
	return nil
}

func (d *Directory) Get(ctx context.Context, sellerID string) (product.SellerRef, error) {
	return d.findOne(ctx, bson.M{"seller_id": sellerID})
}

func (d *Directory) ByUserID(ctx context.Context, userID string) (product.SellerRef, error) {
	return d.findOne(ctx, bson.M{"user_id": userID})
}

func (d *Directory) findOne(ctx context.Context, filter bson.M) (product.SellerRef, error) {
	var doc directoryDocument
	if err := d.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return product.SellerRef{}, product.ErrUnknownSeller
		}
		return product.SellerRef{}, fmt.Errorf("find seller in directory: %w", err)
	}
	return doc.toDomain(), nil
}
