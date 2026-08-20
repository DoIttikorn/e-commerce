// Package sellerv1 is the published contract for the Seller domain.
//
// It lives under api/ with the protobuf contracts because it is the same kind
// of thing: the shape other services are allowed to depend on. Consumers import
// this package; they never import internal/seller.
package sellerv1

import "time"

// TopicSellerEvents carries every event the Seller domain emits. One topic per
// domain rather than per event type keeps ordering meaningful: two events about
// the same seller share a key and therefore a partition.
const TopicSellerEvents = "seller.events"

// Event type discriminators.
const (
	EventSellerRegistered = "seller.registered"
	EventSellerUpdated    = "seller.updated"
)

// SellerEvent is the envelope for everything on TopicSellerEvents.
//
// A single envelope with a Type field, rather than a message per event type,
// so a consumer that only cares about one kind can decode the whole topic and
// ignore the rest without a schema registry.
type SellerEvent struct {
	Type     string `json:"type"`
	SellerID string `json:"seller_id"`

	// UserID is the account that owns the shop. It travels with the event so a
	// consumer can answer "which shop does this caller own?" from its own copy,
	// instead of calling the Seller service on every write.
	UserID string `json:"user_id"`

	ShopName   string    `json:"shop_name"`
	Status     string    `json:"status"`
	OccurredAt time.Time `json:"occurred_at"`
}
