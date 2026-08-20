package product

import "context"

// Repository is the persistence port for products.
type Repository interface {
	Create(ctx context.Context, p Product) (Product, error)
	ByID(ctx context.Context, id string) (Product, error)
	List(ctx context.Context, sellerID string, limit, offset int) (products []Product, total int, err error)
	Update(ctx context.Context, id string, upd Update) (Product, error)
	Delete(ctx context.Context, id string) error

	// RenameSeller applies a seller's new shop name to every product it owns
	// and returns the affected product IDs.
	//
	// The IDs are returned, rather than just a count, so a caching layer can
	// invalidate exactly those entries. The alternative — scanning the cache
	// for keys that might be affected — is the operation that makes Redis
	// unhappy in production.
	RenameSeller(ctx context.Context, sellerID, shopName string) (affected []string, err error)
}

// Update carries only the fields the caller supplied; nil means leave alone.
type Update struct {
	Name        *string
	Description *string
	PriceMinor  *int64
	Stock       *int
}

// IsEmpty reports whether the update would change nothing.
func (u Update) IsEmpty() bool {
	return u.Name == nil && u.Description == nil && u.PriceMinor == nil && u.Stock == nil
}

// SellerDirectory is Product's local read model of the Seller domain, built
// from events rather than from calls.
//
// It is a port like any other: the domain says what it needs to know about
// sellers, and an adapter decides where that comes from.
type SellerDirectory interface {
	// Upsert records what an event said about a seller. It must be idempotent,
	// because at-least-once delivery means the same event can arrive twice.
	Upsert(ctx context.Context, ref SellerRef) error

	// Get returns ErrUnknownSeller if no event has been seen for this seller.
	Get(ctx context.Context, sellerID string) (SellerRef, error)

	// ByUserID answers which shop an account owns, from the same local copy.
	// This is what keeps the product write path free of outbound calls.
	ByUserID(ctx context.Context, userID string) (SellerRef, error)
}
