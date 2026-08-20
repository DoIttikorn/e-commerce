// Package mongodb is the driven adapter for the User domain: it implements
// user.Repository against MongoDB.
//
// It is named mongodb rather than mongo so that it can import the driver
// package it wraps. Everything MongoDB-shaped stops here — the domain sees only
// user.User and the domain errors.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/user"
)

// CollectionName is exported so integration tests can clean up after themselves.
const CollectionName = "users"

// document is the stored shape.
//
// It is a separate type from user.User on purpose: the entity carries no bson
// tags, so a change to the storage layout cannot silently change the domain,
// and the mapping between them is written out where it can be read.
type document struct {
	ID           bson.ObjectID `bson:"_id,omitempty"`
	Name         string        `bson:"name"`
	Email        string        `bson:"email"`
	PasswordHash string        `bson:"password_hash"`
	CreatedAt    time.Time     `bson:"created_at"`
}

func (d document) toDomain() user.User {
	return user.User{
		ID:           d.ID.Hex(),
		Name:         d.Name,
		Email:        d.Email,
		PasswordHash: d.PasswordHash,
		CreatedAt:    d.CreatedAt,
	}
}

// Repository implements user.Repository.
type Repository struct {
	coll *mongo.Collection
}

var _ user.Repository = (*Repository)(nil)

// NewRepository returns a Repository over db's users collection.
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{coll: db.Collection(CollectionName)}
}

// EnsureIndexes creates the indexes this adapter depends on.
//
// It lives here rather than in internal/database because the index belongs to
// the domain that needs it: adding a domain must never mean editing shared
// setup code. Call it once at startup.
//
// The unique index on email is not an optimisation — it is the only thing that
// makes email uniqueness true. An application-level "does this exist?" check
// is passed by both of two concurrent registrations.
func (r *Repository) EnsureIndexes(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("uniq_email"),
	})
	if err != nil {
		return fmt.Errorf("create users email index: %w", err)
	}
	return nil
}

func (r *Repository) Create(ctx context.Context, u user.User) (user.User, error) {
	doc := document{
		Name:         u.Name,
		Email:        u.Email,
		PasswordHash: u.PasswordHash,
		CreatedAt:    u.CreatedAt,
	}

	res, err := r.coll.InsertOne(ctx, doc)
	if err != nil {
		// The duplicate-key error from the unique index is the real uniqueness
		// check; this is where it becomes a domain error.
		if mongo.IsDuplicateKeyError(err) {
			return user.User{}, user.ErrEmailTaken
		}
		return user.User{}, fmt.Errorf("insert user: %w", err)
	}

	id, ok := res.InsertedID.(bson.ObjectID)
	if !ok {
		return user.User{}, fmt.Errorf("insert user: unexpected id type %T", res.InsertedID)
	}
	doc.ID = id
	return doc.toDomain(), nil
}

func (r *Repository) ByID(ctx context.Context, id string) (user.User, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		// A malformed ID is a different failure from one that simply is not
		// there, and the adapters above turn it into a different status.
		return user.User{}, user.ErrInvalidID
	}
	return r.findOne(ctx, bson.M{"_id": oid})
}

func (r *Repository) ByEmail(ctx context.Context, email string) (user.User, error) {
	return r.findOne(ctx, bson.M{"email": email})
}

func (r *Repository) findOne(ctx context.Context, filter bson.M) (user.User, error) {
	var doc document
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return user.User{}, user.ErrUserNotFound
		}
		return user.User{}, fmt.Errorf("find user: %w", err)
	}
	return doc.toDomain(), nil
}

func (r *Repository) List(ctx context.Context, limit, offset int) ([]user.User, int, error) {
	// Exact, because a total that disagrees with the pages makes pagination
	// visibly wrong. Count uses the cheap estimate instead; see below.
	total, err := r.coll.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}

	cursor, err := r.coll.Find(ctx, bson.M{}, options.Find().
		SetLimit(int64(limit)).
		SetSkip(int64(offset)).
		// _id breaks ties: sorting on created_at alone is not a total order,
		// so two users created in the same millisecond could appear on two
		// pages or on none.
		SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}

	var docs []document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, 0, fmt.Errorf("decode users: %w", err)
	}

	users := make([]user.User, 0, len(docs))
	for _, d := range docs {
		users = append(users, d.toDomain())
	}
	return users, int(total), nil
}

func (r *Repository) Update(ctx context.Context, id string, upd user.Update) (user.User, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return user.User{}, user.ErrInvalidID
	}

	set := bson.M{}
	if upd.Name != nil {
		set["name"] = *upd.Name
	}
	if upd.Email != nil {
		set["email"] = *upd.Email
	}
	if len(set) == 0 {
		// The service rejects this first; guarding here too keeps the adapter
		// from issuing an empty $set, which MongoDB rejects with a confusing error.
		return r.ByID(ctx, id)
	}

	var doc document
	err = r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": oid},
		bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&doc)

	switch {
	case mongo.IsDuplicateKeyError(err):
		return user.User{}, user.ErrEmailTaken
	case errors.Is(err, mongo.ErrNoDocuments):
		return user.User{}, user.ErrUserNotFound
	case err != nil:
		return user.User{}, fmt.Errorf("update user: %w", err)
	}
	return doc.toDomain(), nil
}

func (r *Repository) Delete(ctx context.Context, id string) error {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return user.ErrInvalidID
	}

	res, err := r.coll.DeleteOne(ctx, bson.M{"_id": oid})
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if res.DeletedCount == 0 {
		return user.ErrUserNotFound
	}
	return nil
}

// Count backs the periodic user-count log.
//
// CountDocuments is exact, which is what "the total number of users" asks for.
// EstimatedDocumentCount would be cheaper — it reads collection metadata rather
// than scanning the index — and is the right swap if this ever runs against a
// collection large enough for a ten-second timer to matter. It is not that
// today, and an exact number is easier to trust in a log line.
func (r *Repository) Count(ctx context.Context) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.M{})
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}
