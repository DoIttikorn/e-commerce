// Package orderv1 is the published contract for the Order domain.
package orderv1

import "time"

// TopicOrderEvents carries every event the Order domain emits.
const TopicOrderEvents = "order.events"

// Event type discriminators.
const (
	EventOrderPlaced    = "order.placed"
	EventOrderPaid      = "order.paid"
	EventOrderCancelled = "order.cancelled"
)

// OrderLine is one line of an order, as it stood when the order was placed.
type OrderLine struct {
	ProductID   string `json:"product_id"`
	ProductName string `json:"product_name"`
	UnitMinor   int64  `json:"unit_minor"`
	Quantity    int    `json:"quantity"`
}

// OrderEvent is the envelope for everything on TopicOrderEvents.
//
// It carries the whole order rather than just an ID. A consumer building a
// read model — a seller dashboard, a live-commerce ticker — can then act on the
// event alone, without calling back into the Order service and turning one
// event into a request per consumer.
type OrderEvent struct {
	Type        string      `json:"type"`
	OrderID     string      `json:"order_id"`
	BuyerUserID string      `json:"buyer_user_id"`
	SellerID    string      `json:"seller_id"`
	Status      string      `json:"status"`
	TotalMinor  int64       `json:"total_minor"`
	Currency    string      `json:"currency"`
	Lines       []OrderLine `json:"lines"`
	OccurredAt  time.Time   `json:"occurred_at"`
}
