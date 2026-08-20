package product

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	MinNameLen = 2
	MaxNameLen = 200

	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Service holds the business rules.
type Service struct {
	repo      Repository
	directory SellerDirectory
	log       *slog.Logger
}

// NewProduct is the input to Create.
//
// OwnerUserID rather than SellerID: the caller proves who they are with a
// token, and the service works out which shop that is from its own directory.
// A caller cannot list a product in somebody else's shop by guessing an ID.
type NewProduct struct {
	OwnerUserID string
	Name        string
	Description string
	PriceMinor  int64
	Currency    string
	Stock       int
}

// NewService wires the domain to its adapters.
func NewService(repo Repository, directory SellerDirectory, log *slog.Logger) *Service {
	return &Service{repo: repo, directory: directory, log: log}
}

// Create lists a product for sale.
//
// The seller's shop name is taken from the local directory rather than by
// asking the Seller service. That is the whole point of consuming its events:
// this write path makes no outbound call, so it does not slow down or fail
// when another service does.
func (s *Service) Create(ctx context.Context, in NewProduct) (Product, error) {
	fields := map[string]string{}

	if strings.TrimSpace(in.OwnerUserID) == "" {
		fields["owner"] = "is required"
	}

	name := strings.TrimSpace(in.Name)
	switch {
	case name == "":
		fields["name"] = "is required"
	case len(name) < MinNameLen:
		fields["name"] = fmt.Sprintf("must be at least %d characters", MinNameLen)
	case len(name) > MaxNameLen:
		fields["name"] = fmt.Sprintf("must be at most %d characters", MaxNameLen)
	}

	if in.PriceMinor <= 0 {
		fields["price_minor"] = "must be greater than zero"
	}
	if len(in.Currency) != 3 {
		fields["currency"] = "must be a three-letter code, for example THB"
	}
	if in.Stock < 0 {
		fields["stock"] = "must not be negative"
	}
	if err := newValidationError(fields); err != nil {
		return Product{}, err
	}

	ref, err := s.directory.ByUserID(ctx, in.OwnerUserID)
	if err != nil {
		// Includes the seller simply not having arrived over the stream yet,
		// which is the price of asynchronous replication and is worth being
		// explicit about rather than papering over.
		return Product{}, err
	}

	return s.repo.Create(ctx, Product{
		SellerID:    ref.SellerID,
		SellerName:  ref.ShopName,
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		PriceMinor:  in.PriceMinor,
		Currency:    strings.ToUpper(in.Currency),
		Stock:       in.Stock,
		CreatedAt:   time.Now().UTC(),
	})
}

// ByID returns one product.
func (s *Service) ByID(ctx context.Context, id string) (Product, error) {
	return s.repo.ByID(ctx, id)
}

// List returns one page, optionally narrowed to a single seller.
func (s *Service) List(ctx context.Context, sellerID string, limit, offset int) ([]Product, int, error) {
	limit, offset = ClampPage(limit, offset)
	return s.repo.List(ctx, sellerID, limit, offset)
}

// Update changes the fields the caller supplied.
func (s *Service) Update(ctx context.Context, id string, upd Update) (Product, error) {
	if upd.IsEmpty() {
		return Product{}, &ValidationError{Fields: map[string]string{
			"body": "supply at least one field to change",
		}}
	}

	fields := map[string]string{}

	if upd.Name != nil {
		name := strings.TrimSpace(*upd.Name)
		if len(name) < MinNameLen || len(name) > MaxNameLen {
			fields["name"] = fmt.Sprintf("must be between %d and %d characters", MinNameLen, MaxNameLen)
		}
		upd.Name = &name
	}
	if upd.PriceMinor != nil && *upd.PriceMinor <= 0 {
		fields["price_minor"] = "must be greater than zero"
	}
	if upd.Stock != nil && *upd.Stock < 0 {
		fields["stock"] = "must not be negative"
	}
	if err := newValidationError(fields); err != nil {
		return Product{}, err
	}

	return s.repo.Update(ctx, id, upd)
}

// Delete removes a product.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// AuthorizeOwner reports whether the account owns the shop a product sits in.
//
// It lives in the service rather than the handler because it is a rule about
// the domain, and because both driving adapters would otherwise implement it
// twice and eventually differently.
func (s *Service) AuthorizeOwner(ctx context.Context, userID, productID string) error {
	found, err := s.repo.ByID(ctx, productID)
	if err != nil {
		return err
	}

	ref, err := s.directory.ByUserID(ctx, userID)
	if err != nil {
		// No shop for this account, so it cannot own anything.
		if errors.Is(err, ErrUnknownSeller) {
			return ErrNotOwner
		}
		return err
	}
	if ref.SellerID != found.SellerID {
		return ErrNotOwner
	}
	return nil
}

// ApplySellerEvent folds an event from the Seller domain into this service's
// own state: the directory Create reads from, and the copy of the shop name
// carried on every product that seller owns.
//
// It must be idempotent. Delivery is at-least-once, so the same event will be
// seen twice sooner or later — and both steps here are writes that produce the
// same result whether they run once or five times.
func (s *Service) ApplySellerEvent(ctx context.Context, ref SellerRef) error {
	if ref.SellerID == "" {
		// A malformed event must not stall the partition forever, so it is
		// dropped rather than retried. It is logged as an error because an
		// event with no subject means something upstream is wrong.
		s.log.LogAttrs(ctx, slog.LevelError, "seller event without an id, dropping")
		return nil
	}

	if err := s.directory.Upsert(ctx, ref); err != nil {
		return fmt.Errorf("upsert seller directory: %w", err)
	}

	affected, err := s.repo.RenameSeller(ctx, ref.SellerID, ref.ShopName)
	if err != nil {
		return fmt.Errorf("apply seller rename: %w", err)
	}

	if len(affected) > 0 {
		s.log.LogAttrs(ctx, slog.LevelInfo, "seller rename applied to products",
			slog.String("seller_id", ref.SellerID),
			slog.String("shop_name", ref.ShopName),
			slog.Int("products", len(affected)))
	}
	return nil
}

// IsUnknownSeller reports whether err means the seller is not in the directory.
func IsUnknownSeller(err error) bool { return errors.Is(err, ErrUnknownSeller) }

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
