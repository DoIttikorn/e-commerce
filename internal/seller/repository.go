package seller

import (
	"context"
	"time"
)

// Repository is the persistence port, declared by the consumer.
type Repository interface {
	// NextID returns an identifier the store will accept.
	//
	// The service needs it before writing, because the event recorded beside
	// the shop has to carry it.
	NextID() string

	// Create returns ErrShopNameTaken or ErrAlreadySeller, both derived from
	// unique indexes rather than from preceding reads.
	//
	// The events are written in the same transaction as the shop. That is what
	// closes the window where a shop exists and nobody was told — see
	// internal/outbox.
	Create(ctx context.Context, s Seller, events []OutboxEvent) (Seller, error)

	ByID(ctx context.Context, id string) (Seller, error)
	ByUserID(ctx context.Context, userID string) (Seller, error)
	List(ctx context.Context, limit, offset int) (sellers []Seller, total int, err error)
	Update(ctx context.Context, id string, upd Update, events []OutboxEvent) (Seller, error)
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
	ShopName *string
	Status   *Status
}

// IsEmpty reports whether the update would change nothing.
func (u Update) IsEmpty() bool { return u.ShopName == nil && u.Status == nil }
