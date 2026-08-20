package seller

import (
	"context"
	"encoding/json"
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

// Service is everything the Seller domain can do.
//
// Every write that changes a shop announces it, which is the contract Product
// and every future consumer depend on. That is a property of this interface as
// much as of the implementation: a second implementation that stayed silent
// would satisfy the compiler and break the system.
type Service interface {
	// Register opens a shop for an account. Returns ErrAlreadySeller if the
	// account already has one, or ErrShopNameTaken — both from unique indexes.
	// Announces seller.registered.
	Register(ctx context.Context, in NewSeller) (Seller, error)

	// ByID returns one shop, or ErrSellerNotFound / ErrInvalidID.
	ByID(ctx context.Context, id string) (Seller, error)

	// ByUserID returns the shop an account owns, or ErrSellerNotFound.
	ByUserID(ctx context.Context, userID string) (Seller, error)

	// List returns one page and the total count.
	List(ctx context.Context, limit, offset int) (sellers []Seller, total int, err error)

	// Update changes a shop name, a status, or both, and announces
	// seller.updated. A failed write announces nothing.
	Update(ctx context.Context, id string, upd Update) (Seller, error)
}

// service is the implementation.
type service struct {
	repo Repository
	log  *slog.Logger
}

// NewSeller is the input to Register.
type NewSeller struct {
	UserID   string
	ShopName string
}

var _ Service = (*service)(nil)

// NewService wires the domain to its adapters.
//
// There is no publisher here any more. Events are handed to the repository with
// the write and committed alongside it; publishing is the relay's job, and this
// package no longer has a way to lose an event by succeeding at one and failing
// at the other.
func NewService(repo Repository, log *slog.Logger) Service {
	return &service{repo: repo, log: log}
}

// Register opens a shop for a user.
func (s *service) Register(ctx context.Context, in NewSeller) (Seller, error) {
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

	shop := Seller{
		ID:        s.repo.NextID(),
		UserID:    in.UserID,
		ShopName:  name,
		Status:    StatusActive,
		CreatedAt: time.Now().UTC(),
	}

	event, err := s.event(sellerv1.EventSellerRegistered, shop)
	if err != nil {
		return Seller{}, err
	}

	return s.repo.Create(ctx, shop, []OutboxEvent{event})
}

// ByID returns one shop.
func (s *service) ByID(ctx context.Context, id string) (Seller, error) {
	return s.repo.ByID(ctx, id)
}

// ByUserID returns the shop owned by a user.
func (s *service) ByUserID(ctx context.Context, userID string) (Seller, error) {
	return s.repo.ByUserID(ctx, userID)
}

// List returns one page of shops.
func (s *service) List(ctx context.Context, limit, offset int) ([]Seller, int, error) {
	limit, offset = ClampPage(limit, offset)
	return s.repo.List(ctx, limit, offset)
}

// Update changes a shop name, a status, or both, and announces the result.
//
// This is the event that matters to the rest of the system: Product keeps a
// copy of the shop name so a listing does not have to ask who the seller is on
// every read, and this is what keeps that copy honest.
func (s *service) Update(ctx context.Context, id string, upd Update) (Seller, error) {
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

	// The event describes the shop after the change, so it is built from what
	// the update will produce rather than from what is there now.
	current, err := s.repo.ByID(ctx, id)
	if err != nil {
		return Seller{}, err
	}

	event, err := s.event(sellerv1.EventSellerUpdated, apply(current, upd))
	if err != nil {
		return Seller{}, err
	}

	return s.repo.Update(ctx, id, upd, []OutboxEvent{event})
}

// apply projects an update onto a shop, so the event and the row that the
// transaction is about to write describe the same thing.
func apply(s Seller, upd Update) Seller {
	if upd.ShopName != nil {
		s.ShopName = *upd.ShopName
	}
	if upd.Status != nil {
		s.Status = *upd.Status
	}
	return s
}

// event builds an outbox entry describing a shop.
//
// Nothing is published here. The entry is handed to the repository and written
// in the same transaction as the change it describes, so the two cannot
// disagree: either both happened or neither did.
func (s *service) event(eventType string, shop Seller) (OutboxEvent, error) {
	payload, err := json.Marshal(sellerv1.SellerEvent{
		Type:       eventType,
		SellerID:   shop.ID,
		UserID:     shop.UserID,
		ShopName:   shop.ShopName,
		Status:     string(shop.Status),
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("marshal seller event: %w", err)
	}

	// Keyed by seller ID so every event about one shop keeps its order.
	return OutboxEvent{
		Topic:     sellerv1.TopicSellerEvents,
		Key:       shop.ID,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}, nil
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
