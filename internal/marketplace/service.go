package marketplace

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// Page size bounds. Search is public and uncached at the edges, so the upper
// bound is what stops one request asking for the whole catalogue.
const (
	DefaultPageSize = 20
	MaxPageSize     = 60
)

// MaxTextLen caps the search string. A long enough term is a way to make the
// database work hard for nothing.
const MaxTextLen = 200

// ProductChange is what a product event means to this domain.
type ProductChange struct {
	ProductID   string
	SellerID    string
	SellerName  string
	Name        string
	Description string
	PriceMinor  int64
	Currency    string
	Stock       int
	Delisted    bool
}

// SellerChange is what a seller event means to this domain.
type SellerChange struct {
	SellerID string
	ShopName string
	Active   bool
}

// Sale is what an order event means to this domain.
type Sale struct {
	OrderID string
	Lines   []SoldLine
}

// Service is everything the Marketplace domain can do.
//
// One read and three writes, and every write is an event arriving. That ratio
// is the shape of a read model: it is written by the system and read by people.
type Service interface {
	// Search answers a query against the projection.
	Search(ctx context.Context, q Query) (listings []Listing, total int, err error)

	// ApplyProductChange folds in a catalogue event.
	ApplyProductChange(ctx context.Context, c ProductChange) error

	// ApplySellerChange folds in a shop event, updating every listing it owns.
	ApplySellerChange(ctx context.Context, c SellerChange) error

	// ApplySale folds in a completed order, which is what makes ranking by
	// popularity possible.
	ApplySale(ctx context.Context, s Sale) error
}

type service struct {
	repo Repository
	log  *slog.Logger
}

var _ Service = (*service)(nil)

// NewService wires the domain to its adapters.
func NewService(repo Repository, log *slog.Logger) Service {
	return &service{repo: repo, log: log}
}

func (s *service) Search(ctx context.Context, q Query) ([]Listing, int, error) {
	normalized, err := normalize(q)
	if err != nil {
		return nil, 0, err
	}
	return s.repo.Search(ctx, normalized)
}

// normalize clamps and validates a query rather than rejecting it wherever it
// can. A search box is not a form: a nonsense sort should quietly become the
// default, while a price range that cannot match anything is worth saying out
// loud because the caller has made a mistake they can fix.
func normalize(q Query) (Query, error) {
	fields := map[string]string{}

	q.Text = strings.TrimSpace(q.Text)
	if len(q.Text) > MaxTextLen {
		fields["q"] = "is too long"
	}
	if q.MinPriceMinor < 0 || q.MaxPriceMinor < 0 {
		fields["price"] = "must not be negative"
	}
	if q.MaxPriceMinor > 0 && q.MinPriceMinor > q.MaxPriceMinor {
		fields["price"] = "min_price is above max_price, so nothing can match"
	}
	if err := newValidationError(fields); err != nil {
		return Query{}, err
	}

	if !q.Sort.Valid() {
		q.Sort = SortRelevance
	}
	// Relevance needs something to be relevant to.
	if q.Sort == SortRelevance && q.Text == "" {
		q.Sort = SortNewest
	}

	q.Limit, q.Offset = ClampPage(q.Limit, q.Offset)
	return q, nil
}

func (s *service) ApplyProductChange(ctx context.Context, c ProductChange) error {
	if c.ProductID == "" {
		// Nothing to key on. Dropped rather than retried forever, because a
		// message that can never succeed blocks everything behind it.
		s.log.LogAttrs(ctx, slog.LevelError, "product event without an id, dropping")
		return nil
	}

	if c.Delisted {
		return s.repo.RemoveListing(ctx, c.ProductID)
	}

	return s.repo.UpsertListing(ctx, Listing{
		ProductID:   c.ProductID,
		SellerID:    c.SellerID,
		SellerName:  c.SellerName,
		Name:        c.Name,
		Description: c.Description,
		PriceMinor:  c.PriceMinor,
		Currency:    c.Currency,
		// Stock is flattened to a boolean on purpose. The exact count changes
		// on every sale and would rewrite this projection constantly; whether
		// something is buyable is what a search result needs, and the product
		// page has the real number.
		InStock:   c.Stock > 0,
		UpdatedAt: time.Now().UTC(),
	})
}

func (s *service) ApplySellerChange(ctx context.Context, c SellerChange) error {
	if c.SellerID == "" {
		s.log.LogAttrs(ctx, slog.LevelError, "seller event without an id, dropping")
		return nil
	}

	touched, err := s.repo.ApplySellerChange(ctx, c.SellerID, c.ShopName, c.Active)
	if err != nil {
		return err
	}
	if touched > 0 {
		s.log.LogAttrs(ctx, slog.LevelInfo, "seller change applied to listings",
			slog.String("seller_id", c.SellerID), slog.Int64("listings", touched))
	}
	return nil
}

func (s *service) ApplySale(ctx context.Context, sale Sale) error {
	if sale.OrderID == "" || len(sale.Lines) == 0 {
		return nil
	}
	return s.repo.RecordSale(ctx, sale.OrderID, sale.Lines)
}

// ClampPage applies the paging bounds.
func ClampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
