package seller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
)

// Shop name bounds.
const (
	MinShopNameLen = 3
	MaxShopNameLen = 60
)

// Page size bounds for List.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// EventPublisher announces a change to whoever is listening.
//
// Declared here as a port, so the domain neither imports the Kafka client nor
// knows how many consumers there are — which is the entire reason a change here
// does not become a list of outbound calls that grows with every new domain.
type EventPublisher interface {
	Publish(ctx context.Context, topic, key string, payload any) error
}

// Service holds the business rules.
type Service struct {
	repo   Repository
	events EventPublisher
	log    *slog.Logger
}

// NewSeller is the input to Register.
type NewSeller struct {
	UserID   string
	ShopName string
}

// NewService wires the domain to its adapters.
func NewService(repo Repository, events EventPublisher, log *slog.Logger) *Service {
	return &Service{repo: repo, events: events, log: log}
}

// Register opens a shop for a user.
func (s *Service) Register(ctx context.Context, in NewSeller) (Seller, error) {
	fields := map[string]string{}

	if strings.TrimSpace(in.UserID) == "" {
		fields["user_id"] = "is required"
	}
	name := strings.TrimSpace(in.ShopName)
	if msg := checkShopName(name); msg != "" {
		fields["shop_name"] = msg
	}
	if err := newValidationError(fields); err != nil {
		return Seller{}, err
	}

	created, err := s.repo.Create(ctx, Seller{
		UserID:    in.UserID,
		ShopName:  name,
		Status:    StatusActive,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return Seller{}, err
	}

	s.announce(ctx, sellerv1.EventSellerRegistered, created)
	return created, nil
}

// ByID returns one shop.
func (s *Service) ByID(ctx context.Context, id string) (Seller, error) {
	return s.repo.ByID(ctx, id)
}

// ByUserID returns the shop owned by a user.
func (s *Service) ByUserID(ctx context.Context, userID string) (Seller, error) {
	return s.repo.ByUserID(ctx, userID)
}

// List returns one page of shops.
func (s *Service) List(ctx context.Context, limit, offset int) ([]Seller, int, error) {
	limit, offset = ClampPage(limit, offset)
	return s.repo.List(ctx, limit, offset)
}

// Update changes a shop name, a status, or both, and announces the result.
//
// This is the event that matters to the rest of the system: Product keeps a
// copy of the shop name so a listing does not have to ask who the seller is on
// every read, and this is what keeps that copy honest.
func (s *Service) Update(ctx context.Context, id string, upd Update) (Seller, error) {
	if upd.IsEmpty() {
		return Seller{}, &ValidationError{Fields: map[string]string{
			"body": "supply at least one of shop_name or status",
		}}
	}

	fields := map[string]string{}

	if upd.ShopName != nil {
		name := strings.TrimSpace(*upd.ShopName)
		if msg := checkShopName(name); msg != "" {
			fields["shop_name"] = msg
		}
		upd.ShopName = &name
	}
	if upd.Status != nil && !upd.Status.Valid() {
		fields["status"] = fmt.Sprintf("must be %q or %q", StatusActive, StatusSuspended)
	}
	if err := newValidationError(fields); err != nil {
		return Seller{}, err
	}

	updated, err := s.repo.Update(ctx, id, upd)
	if err != nil {
		return Seller{}, err
	}

	s.announce(ctx, sellerv1.EventSellerUpdated, updated)
	return updated, nil
}

// announce publishes an event about a shop.
//
// A failure is logged and swallowed, which is a real limitation and worth
// naming: the write has already committed, so returning an error here would
// report a failure for something that succeeded, while the event is lost
// either way. The correct fix is a transactional outbox — write the event to
// the same database in the same transaction and have a relay publish it — and
// that is the first thing to add when this stops being a demonstration.
func (s *Service) announce(ctx context.Context, eventType string, seller Seller) {
	event := sellerv1.SellerEvent{
		Type:       eventType,
		SellerID:   seller.ID,
		UserID:     seller.UserID,
		ShopName:   seller.ShopName,
		Status:     string(seller.Status),
		OccurredAt: time.Now().UTC(),
	}

	// Keyed by seller ID so every event about one shop keeps its order.
	if err := s.events.Publish(ctx, sellerv1.TopicSellerEvents, seller.ID, event); err != nil {
		s.log.LogAttrs(ctx, slog.LevelError, "publishing seller event failed",
			slog.String("type", eventType),
			slog.String("seller_id", seller.ID),
			slog.String("error", err.Error()))
	}
}

func checkShopName(name string) string {
	switch {
	case name == "":
		return "is required"
	case len(name) < MinShopNameLen:
		return fmt.Sprintf("must be at least %d characters", MinShopNameLen)
	case len(name) > MaxShopNameLen:
		return fmt.Sprintf("must be at most %d characters", MaxShopNameLen)
	default:
		return ""
	}
}

// ClampPage applies the paging bounds. Exported so an adapter can report the
// paging that was actually used.
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
