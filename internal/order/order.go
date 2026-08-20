// Package order is the Order domain: what a buyer has committed to buy.
//
// It is the only domain that makes a synchronous call to another service.
// Everything else here waits for an event, because everything else can; an
// order cannot be confirmed without knowing the stock was secured, and "you
// will find out shortly" is not an answer a buyer can act on.
package order

import (
	"errors"
	"time"
)

// Status is where an order has got to.
type Status string

const (
	// StatusPending means stock is reserved and payment has not arrived.
	StatusPending Status = "pending"
	// StatusPaid means the money is in and the reservation is now a sale.
	StatusPaid Status = "paid"
	// StatusCancelled means the reservation has been released.
	StatusCancelled Status = "cancelled"
)

// Line is one item of an order.
//
// ProductName and UnitMinor are snapshots taken when the order was placed, not
// references. A seller who reprices tomorrow must not change what somebody
// agreed to pay today, and a product deleted next week must not turn a past
// order into a blank row.
type Line struct {
	ProductID   string
	ProductName string
	UnitMinor   int64
	Quantity    int
}

// Subtotal is the line total in minor units.
func (l Line) Subtotal() int64 { return l.UnitMinor * int64(l.Quantity) }

// Order is a buyer's commitment to one seller.
type Order struct {
	ID          string
	BuyerUserID string
	SellerID    string

	// IdempotencyKey is supplied by the caller and unique per order. It is what
	// makes a retried placement return the original order rather than buying
	// the same thing twice.
	IdempotencyKey string

	Lines      []Line
	TotalMinor int64
	Currency   string
	Status     Status
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Total sums the lines. The stored TotalMinor is what the buyer agreed to; this
// exists to compute it once at placement.
func Total(lines []Line) int64 {
	var sum int64
	for _, l := range lines {
		sum += l.Subtotal()
	}
	return sum
}

var (
	ErrOrderNotFound = errors.New("order not found")
	ErrInvalidID     = errors.New("malformed order id")
	ErrNotBuyer      = errors.New("this order belongs to another buyer")

	// ErrNotPending means the order has moved on. Cancelling something already
	// paid is a refund, which is a different operation with different rules.
	ErrNotPending = errors.New("order is no longer pending")

	// ErrOutOfStock is the reservation being refused, surfaced unchanged so the
	// buyer is told the truth rather than given a 500.
	ErrOutOfStock = errors.New("one or more items are out of stock")

	// ErrMixedSellers means the basket spans shops. An order belongs to one
	// seller — splitting a basket is the caller's job, and doing it silently
	// here would hide which shop is fulfilling what.
	ErrMixedSellers = errors.New("all items must come from one seller")
)

// ValidationError reports which fields were rejected and why.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "validation failed" }

func newValidationError(fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: fields}
}
