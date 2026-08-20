package marketplace

import "context"

// Repository is the projection store.
//
// Every method except Search is an event handler's write. There is no Create
// and no Delete taking a caller's intent, because nobody creates a listing —
// listings appear because a product was listed somewhere else.
type Repository interface {
	// Search answers a query. This is the only read, and the reason the rest
	// of the package exists.
	Search(ctx context.Context, q Query) (listings []Listing, total int, err error)

	// UpsertListing applies a product event. Idempotent: the same event twice
	// leaves the same row.
	UpsertListing(ctx context.Context, l Listing) error

	// RemoveListing applies a delisting.
	RemoveListing(ctx context.Context, productID string) error

	// ApplySellerChange updates the shop name and trading status across every
	// listing that seller owns, and returns how many were touched.
	ApplySellerChange(ctx context.Context, sellerID, shopName string, active bool) (int64, error)

	// RecordSale increments the sold counters for an order's lines.
	//
	// It takes an orderID and refuses to count the same one twice, because
	// at-least-once delivery means the same order event will arrive again and
	// a popularity ranking that inflates on redelivery is worse than none.
	RecordSale(ctx context.Context, orderID string, lines []SoldLine) error
}

// SoldLine is one line of a completed order.
type SoldLine struct {
	ProductID string
	Quantity  int
}
