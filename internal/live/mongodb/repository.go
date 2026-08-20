// Package mongodb is the driven adapter for the Live Commerce domain.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/live"
)

// Collection names, exported so integration tests can clean up after themselves.
const (
	CollectionName          = "streams"
	DirectoryCollectionName = "seller_directory"
)

type document struct {
	ID                bson.ObjectID `bson:"_id,omitempty"`
	SellerID          string        `bson:"seller_id"`
	SellerName        string        `bson:"seller_name"`
	Title             string        `bson:"title"`
	Status            string        `bson:"status"`
	FeaturedProductID string        `bson:"featured_product_id"`
	StartedAt         *time.Time    `bson:"started_at"`
	EndedAt           *time.Time    `bson:"ended_at"`
	CreatedAt         time.Time     `bson:"created_at"`
	UpdatedAt         time.Time     `bson:"updated_at"`
}

func (d document) toDomain() live.Stream {
	return live.Stream{
		ID:                d.ID.Hex(),
		SellerID:          d.SellerID,
		SellerName:        d.SellerName,
		Title:             d.Title,
		Status:            live.Status(d.Status),
		FeaturedProductID: d.FeaturedProductID,
		StartedAt:         d.StartedAt,
		EndedAt:           d.EndedAt,
		CreatedAt:         d.CreatedAt,
		UpdatedAt:         d.UpdatedAt,
	}
}

// Repository implements live.Repository.
type Repository struct {
	coll *mongo.Collection
}

var _ live.Repository = (*Repository)(nil)

// NewRepository returns a Repository over db's streams collection.
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{coll: db.Collection(CollectionName)}
}

func (r *Repository) NextID() string { return bson.NewObjectID().Hex() }

// EnsureIndexes creates what the queries need.
func (r *Repository) EnsureIndexes(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			// Listing what is on air now, newest first.
			Keys:    bson.D{{Key: "status", Value: 1}, {Key: "updated_at", Value: -1}},
			Options: options.Index().SetName("status_updated"),
		},
		{
			// "Who is showing this product right now?" — asked once per line of
			// every paid order, so it is worth an index rather than a scan.
			Keys:    bson.D{{Key: "featured_product_id", Value: 1}, {Key: "status", Value: 1}},
			Options: options.Index().SetName("featured_status"),
		},
		{
			Keys:    bson.D{{Key: "seller_id", Value: 1}},
			Options: options.Index().SetName("seller"),
		},
	})
	if err != nil {
		return fmt.Errorf("create stream indexes: %w", err)
	}
	return nil
}

func (r *Repository) Create(ctx context.Context, s live.Stream) (live.Stream, error) {
	oid, err := bson.ObjectIDFromHex(s.ID)
	if err != nil {
		return live.Stream{}, live.ErrInvalidID
	}

	doc := document{
		ID: oid, SellerID: s.SellerID, SellerName: s.SellerName,
		Title: s.Title, Status: string(s.Status),
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return live.Stream{}, fmt.Errorf("insert stream: %w", err)
	}
	return doc.toDomain(), nil
}

func (r *Repository) ByID(ctx context.Context, id string) (live.Stream, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return live.Stream{}, live.ErrInvalidID
	}

	var doc document
	if err := r.coll.FindOne(ctx, bson.M{"_id": oid}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return live.Stream{}, live.ErrStreamNotFound
		}
		return live.Stream{}, fmt.Errorf("find stream: %w", err)
	}
	return doc.toDomain(), nil
}

func (r *Repository) ListLive(ctx context.Context, limit, offset int) ([]live.Stream, int, error) {
	filter := bson.M{"status": string(live.StatusLive)}

	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("count streams: %w", err)
	}

	cursor, err := r.coll.Find(ctx, filter, options.Find().
		SetLimit(int64(limit)).SetSkip(int64(offset)).
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, 0, fmt.Errorf("list streams: %w", err)
	}

	var docs []document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, 0, fmt.Errorf("decode streams: %w", err)
	}

	streams := make([]live.Stream, 0, len(docs))
	for _, d := range docs {
		streams = append(streams, d.toDomain())
	}
	return streams, int(total), nil
}

func (r *Repository) Update(ctx context.Context, id string, upd live.Update) (live.Stream, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return live.Stream{}, live.ErrInvalidID
	}

	now := time.Now().UTC()
	set := bson.M{"updated_at": now}

	if upd.Title != nil {
		set["title"] = *upd.Title
	}
	if upd.FeaturedProductID != nil {
		set["featured_product_id"] = *upd.FeaturedProductID
	}
	if upd.Status != nil {
		set["status"] = string(*upd.Status)
		// The timestamps are derived from the transition rather than passed in,
		// so they cannot disagree with the status they describe.
		switch *upd.Status {
		case live.StatusLive:
			set["started_at"] = now
		case live.StatusEnded:
			set["ended_at"] = now
		}
	}

	var doc document
	err = r.coll.FindOneAndUpdate(ctx, bson.M{"_id": oid}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&doc)

	if errors.Is(err, mongo.ErrNoDocuments) {
		return live.Stream{}, live.ErrStreamNotFound
	}
	if err != nil {
		return live.Stream{}, fmt.Errorf("update stream: %w", err)
	}
	return doc.toDomain(), nil
}

func (r *Repository) ApplySellerChange(ctx context.Context, sellerID, shopName string) (int64, error) {
	res, err := r.coll.UpdateMany(ctx,
		bson.M{"seller_id": sellerID},
		bson.M{"$set": bson.M{"seller_name": shopName}})
	if err != nil {
		return 0, fmt.Errorf("apply seller change: %w", err)
	}
	return res.ModifiedCount, nil
}

func (r *Repository) LiveFeaturing(ctx context.Context, productID string) ([]live.Stream, error) {
	cursor, err := r.coll.Find(ctx, bson.M{
		"featured_product_id": productID,
		"status":              string(live.StatusLive),
	})
	if err != nil {
		return nil, fmt.Errorf("find live streams featuring %s: %w", productID, err)
	}

	var docs []document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode streams: %w", err)
	}

	streams := make([]live.Stream, 0, len(docs))
	for _, d := range docs {
		streams = append(streams, d.toDomain())
	}
	return streams, nil
}
