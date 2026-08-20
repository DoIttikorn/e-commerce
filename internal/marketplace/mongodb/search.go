package mongodb

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/marketplace"
)

// Search answers a query against the projection.
//
// Everything here is one collection and one index lookup, which is the reason
// the projection exists at all: the same answer assembled from the Product,
// Seller and Order services would be three calls and a sort in application
// memory, per page, per user.
func (r *Repository) Search(ctx context.Context, q marketplace.Query) ([]marketplace.Listing, int, error) {
	filter := buildFilter(q)

	total, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("count listings: %w", err)
	}

	opts := options.Find().SetLimit(int64(q.Limit)).SetSkip(int64(q.Offset))

	if q.Sort == marketplace.SortRelevance && q.Text != "" {
		// Sorting by text score needs the score projected first; MongoDB will
		// not sort by a field that is not in the document.
		opts = opts.
			SetProjection(bson.M{"score": bson.M{"$meta": "textScore"}}).
			SetSort(bson.M{"score": bson.M{"$meta": "textScore"}})
	} else {
		opts = opts.SetSort(sortSpec(q.Sort))
	}

	cursor, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, fmt.Errorf("search listings: %w", err)
	}

	var docs []document
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, 0, fmt.Errorf("decode listings: %w", err)
	}

	listings := make([]marketplace.Listing, 0, len(docs))
	for _, d := range docs {
		listings = append(listings, d.toDomain())
	}
	return listings, int(total), nil
}

func buildFilter(q marketplace.Query) bson.M {
	filter := bson.M{}

	if q.Text != "" {
		filter["$text"] = bson.M{"$search": q.Text}
	}
	if q.SellerID != "" {
		filter["seller_id"] = q.SellerID
	}
	if q.InStockOnly {
		filter["in_stock"] = true
	}

	// A suspended shop's products stay in the projection but leave the results.
	// Deleting them would mean rebuilding from the event stream if the
	// suspension were lifted, which is work for a state that is usually
	// temporary.
	filter["seller_active"] = true

	price := bson.M{}
	if q.MinPriceMinor > 0 {
		price["$gte"] = q.MinPriceMinor
	}
	if q.MaxPriceMinor > 0 {
		price["$lte"] = q.MaxPriceMinor
	}
	if len(price) > 0 {
		filter["price_minor"] = price
	}
	return filter
}

// sortSpec always ends with _id.
//
// Sorting by price alone is not a total order — a hundred products at the same
// price have no defined sequence — so a page boundary could show the same item
// twice and skip another. The tiebreaker costs nothing and removes the class
// of bug entirely.
func sortSpec(s marketplace.Sort) bson.D {
	switch s {
	case marketplace.SortPriceAsc:
		return bson.D{{Key: "price_minor", Value: 1}, {Key: "_id", Value: 1}}
	case marketplace.SortPriceDesc:
		return bson.D{{Key: "price_minor", Value: -1}, {Key: "_id", Value: 1}}
	case marketplace.SortBestSelling:
		return bson.D{{Key: "sold_count", Value: -1}, {Key: "_id", Value: 1}}
	default:
		return bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: 1}}
	}
}
