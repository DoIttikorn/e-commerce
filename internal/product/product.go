// Package product is the Product domain: the things sellers list for sale.
package product

import (
	"errors"
	"time"
)

// Product is an item for sale.
//
// SellerName is a copy of a fact that belongs to the Seller domain. Holding it
// here is deliberate: a listing page renders hundreds of products, and asking
// the Seller service who each one belongs to would be hundreds of calls to
// render one page. The copy is kept honest by events, not by a join.
type Product struct {
	ID          string
	SellerID    string
	SellerName  string
	Name        string
	Description string

	// PriceMinor is in minor units — satang, not baht. Money is never a float:
	// 0.1 + 0.2 is not 0.3 in binary floating point, and a cent lost per order
	// is a reconciliation problem nobody enjoys.
	PriceMinor int64
	Currency   string

	Stock     int
	CreatedAt time.Time
}

// SellerRef is Product's own record of the seller facts it needs.
//
// It is not seller.Seller. Sharing that type would couple the two domains and
// make them impossible to deploy apart, which is the thing this layout is for.
type SellerRef struct {
	SellerID string
	UserID   string
	ShopName string
	Status   string
}

var (
	ErrProductNotFound = errors.New("product not found")
	ErrInvalidID       = errors.New("malformed product id")

	// ErrUnknownSeller means the seller has not arrived over the event stream
	// yet, or does not exist. Both are the caller's problem to retry.
	ErrUnknownSeller = errors.New("unknown seller")

	// ErrNotOwner means the caller does not own the shop this product is in.
	ErrNotOwner = errors.New("this product belongs to another shop")

	// ErrInsufficientStock means at least one line could not be satisfied. It
	// carries no detail about which: a reservation is all-or-nothing, so a
	// partial answer would be misleading.
	ErrInsufficientStock = errors.New("insufficient stock")

	// ErrMixedSellers means the lines span more than one shop. An order belongs
	// to one seller, so splitting a basket is the caller's job.
	ErrMixedSellers = errors.New("all items must belong to one seller")
)

// ReserveItem is one line of a reservation request.
type ReserveItem struct {
	ProductID string
	Quantity  int
}

// ReservedItem is what was actually taken.
//
// It carries the name and price as they were at the moment of reservation.
// The order snapshots them: a product repriced tomorrow must not change what
// somebody agreed to pay today.
type ReservedItem struct {
	ProductID   string
	ProductName string
	SellerID    string
	UnitMinor   int64
	Currency    string
	Quantity    int
}

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
