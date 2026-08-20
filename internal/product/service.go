package product

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	productv1 "github.com/DoIttikorn/e-commerce/api/product/v1"
)

const (
	MinNameLen = 2
	MaxNameLen = 200

	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Service is everything the Product domain can do.
//
// Note what is not here: nothing that calls another service. Create resolves
// the shop from this domain's own copy, and that copy is maintained by
// ApplySellerEvent — the only method a human never invokes, because the event
// stream does.
type Service interface {
	// Create lists a product in the caller's own shop. Returns
	// ErrUnknownSeller when no seller event has arrived for that account yet,
	// which is the visible cost of replicating asynchronously.
	Create(ctx context.Context, in NewProduct) (Product, error)

	// ByID returns one product, or ErrProductNotFound / ErrInvalidID. This is
	// the read the Redis decorator serves.
	ByID(ctx context.Context, id string) (Product, error)

	// List returns one page, optionally narrowed to a single seller.
	List(ctx context.Context, sellerID string, limit, offset int) (products []Product, total int, err error)

	// Update applies only the fields upd carries.
	Update(ctx context.Context, id string, upd Update) (Product, error)

	Delete(ctx context.Context, id string) error

	// AuthorizeOwner returns ErrNotOwner unless the account owns the shop the
	// product sits in. It lives here rather than in an adapter because it is a
	// rule about the domain, and both adapters would otherwise implement it
	// twice and eventually differently.
	AuthorizeOwner(ctx context.Context, userID, productID string) error

	// Reserve takes stock for an order, all lines or none. The key is the
	// order ID, so a retried call takes stock once.
	Reserve(ctx context.Context, key string, items []ReserveItem) ([]ReservedItem, error)

	// Confirm marks a reservation as belonging to a real order, so the reaper
	// leaves it alone. Callers must do this once the order is written.
	Confirm(ctx context.Context, key string) error

	// ReleaseExpired puts back stock held by reservations nobody confirmed.
	// It is the reaper, and it is what closes the crash window in the caller's
	// saga — a window no logic in the caller can close, because the caller is
	// the thing that died.
	ReleaseExpired(ctx context.Context, olderThan time.Duration) (int, error)

	// Release is the compensating action for a reservation being undone. It is
	// safe to call more than once.
	Release(ctx context.Context, key string, items []ReserveItem) error

	// ApplySellerEvent folds a Seller domain event into this domain's state.
	// It is idempotent, because at-least-once delivery makes repeats certain.
	ApplySellerEvent(ctx context.Context, ref SellerRef) error
}

// service is the implementation.
type service struct {
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

var _ Service = (*service)(nil)

// NewService wires the domain to its adapters.
// There is no publisher here. Events are handed to the repository with the
// write and committed alongside it; publishing is the relay's job.
func NewService(repo Repository, directory SellerDirectory, log *slog.Logger) Service {
	return &service{repo: repo, directory: directory, log: log}
}

// Create lists a product for sale.
//
// The seller's shop name is taken from the local directory rather than by
// asking the Seller service. That is the whole point of consuming its events:
// this write path makes no outbound call, so it does not slow down or fail
// when another service does.
func (s *service) Create(ctx context.Context, in NewProduct) (Product, error) {
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

	listing := Product{
		ID:          s.repo.NextID(),
		SellerID:    ref.SellerID,
		SellerName:  ref.ShopName,
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		PriceMinor:  in.PriceMinor,
		Currency:    strings.ToUpper(in.Currency),
		Stock:       in.Stock,
		CreatedAt:   time.Now().UTC(),
	}

	event, err := s.event(productv1.EventProductListed, listing)
	if err != nil {
		return Product{}, err
	}

	return s.repo.Create(ctx, listing, []OutboxEvent{event})
}

// event builds an outbox entry describing a listing. Nothing is published here.
func (s *service) event(eventType string, p Product) (OutboxEvent, error) {
	payload, err := json.Marshal(productv1.ProductEvent{
		Type:        eventType,
		ProductID:   p.ID,
		SellerID:    p.SellerID,
		SellerName:  p.SellerName,
		Name:        p.Name,
		Description: p.Description,
		PriceMinor:  p.PriceMinor,
		Currency:    p.Currency,
		Stock:       p.Stock,
		OccurredAt:  time.Now().UTC(),
	})
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("marshal product event: %w", err)
	}

	// Keyed by product so a listing's own events stay in order.
	return OutboxEvent{
		Topic:     productv1.TopicProductEvents,
		Key:       p.ID,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// ByID returns one product.
func (s *service) ByID(ctx context.Context, id string) (Product, error) {
	return s.repo.ByID(ctx, id)
}

// List returns one page, optionally narrowed to a single seller.
func (s *service) List(ctx context.Context, sellerID string, limit, offset int) ([]Product, int, error) {
	limit, offset = ClampPage(limit, offset)
	return s.repo.List(ctx, sellerID, limit, offset)
}

// Update changes the fields the caller supplied.
func (s *service) Update(ctx context.Context, id string, upd Update) (Product, error) {
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

	// The event describes the listing after the change, so it is built from
	// what the update will produce rather than from what is there now.
	current, err := s.repo.ByID(ctx, id)
	if err != nil {
		return Product{}, err
	}

	event, err := s.event(productv1.EventProductUpdated, applyUpdate(current, upd))
	if err != nil {
		return Product{}, err
	}

	return s.repo.Update(ctx, id, upd, []OutboxEvent{event})
}

// applyUpdate projects an update onto a product, so the event and the row the
// transaction is about to write describe the same thing.
func applyUpdate(p Product, upd Update) Product {
	if upd.Name != nil {
		p.Name = *upd.Name
	}
	if upd.Description != nil {
		p.Description = *upd.Description
	}
	if upd.PriceMinor != nil {
		p.PriceMinor = *upd.PriceMinor
	}
	if upd.Stock != nil {
		p.Stock = *upd.Stock
	}
	return p
}

// Delete removes a product.
func (s *service) Delete(ctx context.Context, id string) error {
	// Read first, so the delisting event can carry enough for a consumer to
	// find its own copy without asking anybody.
	found, err := s.repo.ByID(ctx, id)
	if err != nil {
		return err
	}

	event, err := s.event(productv1.EventProductDelisted, found)
	if err != nil {
		return err
	}

	return s.repo.Delete(ctx, id, []OutboxEvent{event})
}

// Reserve takes stock for an order.
//
// The domain adds almost nothing to what the repository does, and that is the
// point: the correctness lives in one conditional update inside one
// transaction, where it can be reasoned about, rather than being spread across
// a service that reads and then writes.
func (s *service) Reserve(ctx context.Context, key string, items []ReserveItem) ([]ReservedItem, error) {
	if len(items) == 0 {
		return nil, &ValidationError{Fields: map[string]string{"items": "at least one item is required"}}
	}
	return s.repo.Reserve(ctx, key, items)
}

// Release returns reserved stock.
func (s *service) Release(ctx context.Context, key string, items []ReserveItem) error {
	return s.repo.Release(ctx, key, items)
}

// Confirm marks a reservation as accounted for.
func (s *service) Confirm(ctx context.Context, key string) error {
	return s.repo.Confirm(ctx, key)
}

// ReleaseExpired reclaims stock nobody ever claimed properly.
func (s *service) ReleaseExpired(ctx context.Context, olderThan time.Duration) (int, error) {
	released, err := s.repo.ReleaseExpired(ctx, olderThan)
	if err != nil {
		return 0, err
	}
	if released > 0 {
		// Worth logging loudly. A steady trickle is abandoned checkouts; a
		// sudden spike means callers are crashing after reserving.
		s.log.LogAttrs(ctx, slog.LevelWarn, "released stock from unconfirmed reservations",
			slog.Int("reservations", released))
	}
	return released, nil
}

// AuthorizeOwner reports whether the account owns the shop a product sits in.
//
// It lives in the service rather than the handler because it is a rule about
// the domain, and because both driving adapters would otherwise implement it
// twice and eventually differently.
func (s *service) AuthorizeOwner(ctx context.Context, userID, productID string) error {
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
func (s *service) ApplySellerEvent(ctx context.Context, ref SellerRef) error {
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
