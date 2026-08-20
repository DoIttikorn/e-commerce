package productv1

import "time"

// TopicProductEvents carries every event the Product domain emits.
const TopicProductEvents = "product.events"

// Event type discriminators.
const (
	EventProductListed   = "product.listed"
	EventProductUpdated  = "product.updated"
	EventProductDelisted = "product.delisted"
)

// ProductEvent is the envelope for everything on TopicProductEvents.
//
// It carries the whole listing rather than an ID, so a consumer building a
// searchable catalogue has everything it needs from the event alone. Sending
// only an ID would turn one event into a call back per consumer, which is the
// coupling the event was meant to remove.
type ProductEvent struct {
	Type        string    `json:"type"`
	ProductID   string    `json:"product_id"`
	SellerID    string    `json:"seller_id"`
	SellerName  string    `json:"seller_name"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	PriceMinor  int64     `json:"price_minor"`
	Currency    string    `json:"currency"`
	Stock       int       `json:"stock"`
	OccurredAt  time.Time `json:"occurred_at"`
}
