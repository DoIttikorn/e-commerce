package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/live"
)

type directoryDocument struct {
	SellerID string `bson:"seller_id"`
	UserID   string `bson:"user_id"`
	ShopName string `bson:"shop_name"`
}

// Directory is Live's own copy of who owns which shop, built from events.
type Directory struct {
	coll *mongo.Collection
}

var _ live.SellerDirectory = (*Directory)(nil)

// NewDirectory returns a Directory over db.
func NewDirectory(db *mongo.Database) *Directory {
	return &Directory{coll: db.Collection(DirectoryCollectionName)}
}

// EnsureIndexes makes seller_id unique and user_id searchable.
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
		return fmt.Errorf("create live directory indexes: %w", err)
	}
	return nil
}

func (d *Directory) Upsert(ctx context.Context, ref live.SellerRef) error {
	_, err := d.coll.UpdateOne(ctx,
		bson.M{"seller_id": ref.SellerID},
		bson.M{"$set": bson.M{"user_id": ref.UserID, "shop_name": ref.ShopName}},
		options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert live directory: %w", err)
	}
	return nil
}

func (d *Directory) ByUserID(ctx context.Context, userID string) (live.SellerRef, error) {
	var doc directoryDocument
	if err := d.coll.FindOne(ctx, bson.M{"user_id": userID}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return live.SellerRef{}, live.ErrUnknownSeller
		}
		return live.SellerRef{}, fmt.Errorf("find seller in live directory: %w", err)
	}
	return live.SellerRef{SellerID: doc.SellerID, UserID: doc.UserID, ShopName: doc.ShopName}, nil
}
