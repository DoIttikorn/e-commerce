// Package mongodb is the driven adapter for the Seller domain.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/seller"
)

// CollectionName is exported so integration tests can clean up after themselves.
const CollectionName = "sellers"

// Index names. They are matched against the duplicate-key error to work out
// which constraint was violated, so they must stay in step with the errors below.
const (
	indexUserID   = "uniq_user_id"
	indexShopName = "uniq_shop_name_key"
)

// document is the stored shape.
//
// ShopNameKey exists only for the unique index: shop names are compared without
// regard to case, but displayed as the owner typed them. Deriving the key here
// rather than in the domain is deliberate — it is a storage concern, and the
// entity has no business carrying a field that exists to satisfy an index.
type document struct {
	ID          bson.ObjectID `bson:"_id,omitempty"`
	UserID      string        `bson:"user_id"`
	ShopName    string        `bson:"shop_name"`
	ShopNameKey string        `bson:"shop_name_key"`
	Status      string        `bson:"status"`
	CreatedAt   time.Time     `bson:"created_at"`
}

func (d document) toDomain() seller.Seller {
	return seller.Seller{
		ID:        d.ID.Hex(),
		UserID:    d.UserID,
		ShopName:  d.ShopName,
		Status:    seller.Status(d.Status),
		CreatedAt: d.CreatedAt,
	}
}

func shopNameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Repository implements seller.Repository.
type Repository struct {
	coll *mongo.Collection
}

var _ seller.Repository = (*Repository)(nil)

// NewRepository returns a Repository over db's sellers collection.
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{coll: db.Collection(CollectionName)}
}

// EnsureIndexes creates the two constraints the domain depends on: one shop per
// account, and shop names unique regardless of case. Both are enforced by the
// database because an application-level check races.
func (r *Repository) EnsureIndexes(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "user_id", Value: 1}},
			Options: options.Index().SetUnique(true).SetName(indexUserID),
		},
		{
			Keys:    bson.D{{Key: "shop_name_key", Value: 1}},
			Options: options.Index().SetUnique(true).SetName(indexShopName),
		},
	})
	if err != nil {
		return fmt.Errorf("create seller indexes: %w", err)
	}
	return nil
}

// duplicateError works out which unique index rejected a write.
//
// The driver reports a duplicate key without saying which constraint it was, so
// the index name is read out of the server's message. That is stringly typed
// and would break if the index were renamed, which is why the names are
// constants used both here and at creation.
func duplicateError(err error) error {
	if !mongo.IsDuplicateKeyError(err) {
		return nil
	}
	switch {
	case strings.Contains(err.Error(), indexShopName):
		return seller.ErrShopNameTaken
	case strings.Contains(err.Error(), indexUserID):
		return seller.ErrAlreadySeller
	default:
		return seller.ErrShopNameTaken
	}
}

func (r *Repository) Create(ctx context.Context, s seller.Seller) (seller.Seller, error) {
	doc := document{
		UserID:      s.UserID,
		ShopName:    s.ShopName,
		ShopNameKey: shopNameKey(s.ShopName),
		Status:      string(s.Status),
		CreatedAt:   s.CreatedAt,
	}

	res, err := r.coll.InsertOne(ctx, doc)
	if err != nil {
		if dup := duplicateError(err); dup != nil {
			return seller.Seller{}, dup
		}
		return seller.Seller{}, fmt.Errorf("insert seller: %w", err)
	}

	id, ok := res.InsertedID.(bson.ObjectID)
	if !ok {
		return seller.Seller{}, fmt.Errorf("insert seller: unexpected id type %T", res.InsertedID)
	}
	doc.ID = id
	return doc.toDomain(), nil
}

func (r *Repository) ByID(ctx context.Context, id string) (seller.Seller, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return seller.Seller{}, seller.ErrInvalidID
	}
	return r.findOne(ctx, bson.M{"_id": oid})
}

func (r *Repository) ByUserID(ctx context.Context, userID string) (seller.Seller, error) {
	return r.findOne(ctx, bson.M{"user_id": userID})
}

func (r *Repository) findOne(ctx context.Context, filter bson.M) (seller.Seller, error) {
	var doc document
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return seller.Seller{}, seller.ErrSellerNotFound
		}
		return seller.Seller{}, fmt.Errorf("find seller: %w", err)
	}
	return doc.toDomain(), nil
}

func (r *Repository) List(ctx context.Context, limit, offset int) ([]seller.Seller, int, error) {
	total, err := r.coll.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, 0, fmt.Errorf("count sellers: %w", err)
	}

	cursor, err := r.coll.Find(ctx, bson.M{}, options.Find().
		SetLimit(int64(limit)).
		SetSkip(int64(offset)).
		// _id breaks ties, so two shops created in the same millisecond cannot
		// appear on two pages or on none.
		SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, 0, fmt.Errorf("list sellers: %w", err)
	}

	var docs []document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, 0, fmt.Errorf("decode sellers: %w", err)
	}

	sellers := make([]seller.Seller, 0, len(docs))
	for _, d := range docs {
		sellers = append(sellers, d.toDomain())
	}
	return sellers, int(total), nil
}

func (r *Repository) Update(ctx context.Context, id string, upd seller.Update) (seller.Seller, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return seller.Seller{}, seller.ErrInvalidID
	}

	set := bson.M{}
	if upd.ShopName != nil {
		set["shop_name"] = *upd.ShopName
		set["shop_name_key"] = shopNameKey(*upd.ShopName)
	}
	if upd.Status != nil {
		set["status"] = string(*upd.Status)
	}
	if len(set) == 0 {
		return r.ByID(ctx, id)
	}

	var doc document
	err = r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": oid},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&doc)

	switch {
	case duplicateError(err) != nil:
		return seller.Seller{}, duplicateError(err)
	case errors.Is(err, mongo.ErrNoDocuments):
		return seller.Seller{}, seller.ErrSellerNotFound
	case err != nil:
		return seller.Seller{}, fmt.Errorf("update seller: %w", err)
	}
	return doc.toDomain(), nil
}
