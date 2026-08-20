// Package marketplace is the read side: one searchable view of what every shop
// is selling.
//
// It owns no truth. Every row in it was put there by an event from Product,
// Seller or Order, and it answers questions none of those three can answer
// alone — "the best-selling mugs under 300 baht, from shops that are still
// trading" spans all of them.
//
// That is the shape worth noticing. A read model is not a cache of another
// service; it is a different arrangement of the same facts, built for a
// question the source was not shaped to answer.
package marketplace

import (
	"errors"
	"time"
)

// Listing is one product as the marketplace shows it.
//
// SellerName and SoldCount come from other domains entirely. Denormalising
// them is the whole point: a search that had to join three services per result
// would be a search nobody waits for.
type Listing struct {
	ProductID    string
	SellerID     string
	SellerName   string
	SellerActive bool

	Name        string
	Description string
	PriceMinor  int64
	Currency    string
	InStock     bool

	// SoldCount is accumulated from order events. It is what makes ranking by
	// popularity possible without asking the Order service anything.
	SoldCount int64

	UpdatedAt time.Time
}

// Sort is how results are ordered.
type Sort string

const (
	// SortRelevance uses the text score, and falls back to recency when there
	// is no query to score against.
	SortRelevance   Sort = "relevance"
	SortNewest      Sort = "newest"
	SortPriceAsc    Sort = "price_asc"
	SortPriceDesc   Sort = "price_desc"
	SortBestSelling Sort = "best_selling"
)

// Valid reports whether s is a sort the domain recognises.
func (s Sort) Valid() bool {
	switch s {
	case SortRelevance, SortNewest, SortPriceAsc, SortPriceDesc, SortBestSelling:
		return true
	default:
		return false
	}
}

// Query is a search.
type Query struct {
	// Text is matched against name and description. Empty means browse.
	Text string

	SellerID string

	// MinPriceMinor and MaxPriceMinor are inclusive bounds in minor units.
	// Zero means unbounded, which is why they are not pointers: a price of
	// zero is not a thing anybody sells.
	MinPriceMinor int64
	MaxPriceMinor int64

	// InStockOnly hides listings whose stock reached zero.
	InStockOnly bool

	Sort   Sort
	Limit  int
	Offset int
}

var (
	ErrInvalidQuery = errors.New("invalid query")
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
