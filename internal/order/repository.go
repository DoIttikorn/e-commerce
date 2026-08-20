package order

import (
	"context"
	"time"
)

// Repository is the persistence port.
type Repository interface {
	// NextID returns an identifier the store will accept.
	//
	// The service needs the ID before it writes, because the event it records
	// alongside the order has to carry it. Asking the store to mint one is
	// cleaner than writing first and then editing the event to add it.
	NextID() string

	// Save writes the order and the events it produced in one transaction.
	//
	// The two together are the point. Writing the order and then publishing
	// leaves a window where the order exists and nobody was told; writing the
	// event to the same database in the same transaction closes it, and a relay
	// publishes afterwards. That is the transactional outbox, and it is why
	// this method takes events rather than the service publishing them itself.
	Save(ctx context.Context, o Order, events []OutboxEvent) (Order, error)

	// ByIdempotencyKey returns a previously placed order, if this key has been
	// used. Returns ErrOrderNotFound when it has not.
	ByIdempotencyKey(ctx context.Context, key string) (Order, error)

	ByID(ctx context.Context, id string) (Order, error)

	// ListForBuyer returns one page of a buyer's own orders.
	ListForBuyer(ctx context.Context, buyerUserID string, limit, offset int) (orders []Order, total int, err error)

	// UpdateStatus moves an order on, and records the resulting event in the
	// same transaction for the same reason Save does.
	UpdateStatus(ctx context.Context, id string, from, to Status, events []OutboxEvent) (Order, error)
}

// OutboxEvent is one event waiting to be published.
type OutboxEvent struct {
	Topic     string
	Key       string
	Payload   []byte
	CreatedAt time.Time
}

// StockReserver is the port for the one synchronous call this domain makes.
//
// Declared here, at the consumer, and deliberately narrow: the Order domain
// needs to take stock and to give it back, and knows nothing about catalogues,
// prices or shops beyond what a reservation hands back.
type StockReserver interface {
	// Reserve takes stock for every line or none. key is the order's
	// idempotency key, so a retried call takes stock once.
	Reserve(ctx context.Context, key string, items []ReserveLine) ([]ReservedLine, error)

	// Release gives stock back. It must be safe to call more than once,
	// because compensation runs on the path where retries happen.
	Release(ctx context.Context, key string, items []ReserveLine) error
}

// ReserveLine is what the Order domain asks for.
type ReserveLine struct {
	ProductID string
	Quantity  int
}

// ReservedLine is what it got, priced and named at the moment of reservation.
type ReservedLine struct {
	ProductID   string
	ProductName string
	SellerID    string
	UnitMinor   int64
	Currency    string
	Quantity    int
}
