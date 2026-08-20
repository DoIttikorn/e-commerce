package product

import (
	"context"
	"time"
)

// Repository is the persistence port for products.
//
// The write methods take the events the change produced and commit them in the
// same transaction. That is what stops a listing existing while nobody was
// told — see internal/outbox.
type Repository interface {
	// NextID returns an identifier the store will accept, so the event written
	// beside a product can carry the product's ID.
	NextID() string

	Create(ctx context.Context, p Product, events []OutboxEvent) (Product, error)
	ByID(ctx context.Context, id string) (Product, error)
	List(ctx context.Context, sellerID string, limit, offset int) (products []Product, total int, err error)
	Update(ctx context.Context, id string, upd Update, events []OutboxEvent) (Product, error)
	Delete(ctx context.Context, id string, events []OutboxEvent) error

	// Reserve takes stock for every item or none of them, and records key so a
	// repeat of the same key takes stock once.
	//
	// Returns ErrInsufficientStock if any line cannot be satisfied. A retry
	// after a network timeout is expected — the caller cannot tell whether the
	// server acted — so this must be idempotent rather than merely correct.
	Reserve(ctx context.Context, key string, items []ReserveItem) ([]ReservedItem, error)

	// Confirm marks a reservation as belonging to a real order.
	//
	// Reservation is two-phase for one reason: without it, nothing can tell a
	// reservation whose order is still being written from one whose order never
	// happened because the caller crashed. Confirming says "this one is
	// accounted for"; anything left unconfirmed is, after a while, evidence of
	// a caller that went away.
	//
	// Confirming an unknown or already-confirmed key is not an error.
	Confirm(ctx context.Context, key string) error

	// ReleaseExpired puts back stock held by reservations that were never
	// confirmed. It returns how many it released.
	ReleaseExpired(ctx context.Context, olderThan time.Duration) (int, error)

	// Release puts stock back, and is the compensating action for a reservation
	// that has to be undone. Releasing a key that was never reserved, or was
	// already released, is not an error: compensation runs on the unhappy path,
	// which is exactly where retries happen.
	Release(ctx context.Context, key string, items []ReserveItem) error

	// RenameSeller applies a seller's new shop name to every product it owns
	// and returns the affected product IDs.
	//
	// The IDs are returned, rather than just a count, so a caching layer can
	// invalidate exactly those entries. The alternative — scanning the cache
	// for keys that might be affected — is the operation that makes Redis
	// unhappy in production.
	RenameSeller(ctx context.Context, sellerID, shopName string) (affected []string, err error)
}

// OutboxEvent is one event waiting to be published.
type OutboxEvent struct {
	Topic     string
	Key       string
	Payload   []byte
	CreatedAt time.Time
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
