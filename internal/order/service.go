package order

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	orderv1 "github.com/DoIttikorn/e-commerce/api/order/v1"
)

// Page size bounds for ListForBuyer.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// MaxLines caps a single order. An unbounded basket means an unbounded
// reservation transaction, and a transaction touching hundreds of documents is
// a lock-contention problem waiting for a busy afternoon.
const MaxLines = 50

// Service is everything the Order domain can do.
type Service interface {
	// Place reserves stock and records the order, or does neither.
	Place(ctx context.Context, in NewOrder) (Placement, error)

	// ByID returns one order, or ErrOrderNotFound / ErrInvalidID.
	ByID(ctx context.Context, id string) (Order, error)

	// ListForBuyer returns one page of a buyer's own orders.
	ListForBuyer(ctx context.Context, buyerUserID string, limit, offset int) (orders []Order, total int, err error)

	// Cancel releases the reservation and closes the order. Only the buyer may,
	// and only while it is still pending.
	Cancel(ctx context.Context, id, buyerUserID string) (Order, error)

	// MarkPaid turns a reservation into a sale.
	MarkPaid(ctx context.Context, id, buyerUserID string) (Order, error)
}

// Placement is the result of Place.
//
// Replayed says whether this call did anything. A caller retrying a timed-out
// request needs to tell "I placed it" from "it was already placed", and
// guessing from timestamps is the kind of inference that is right until it
// quietly is not.
type Placement struct {
	Order    Order
	Replayed bool
}

// NewOrder is the input to Place.
type NewOrder struct {
	BuyerUserID string

	// IdempotencyKey is required, not generated. Placing an order is the one
	// operation here that spends money, and a caller that cannot safely retry
	// a timed-out request will either lose orders or place them twice. Making
	// the caller supply the key forces that decision to be made rather than
	// assumed.
	IdempotencyKey string

	Lines []NewLine
}

// NewLine is a requested quantity of a product. No price: the buyer does not
// get to name it, and the price that counts is the one at reservation time.
type NewLine struct {
	ProductID string
	Quantity  int
}

type service struct {
	repo  Repository
	stock StockReserver
	log   *slog.Logger
}

var _ Service = (*service)(nil)

// NewService wires the domain to its adapters.
func NewService(repo Repository, stock StockReserver, log *slog.Logger) Service {
	return &service{repo: repo, stock: stock, log: log}
}

// Place is a saga, not a transaction.
//
// Stock lives in another service with its own database, so there is no
// transaction that could span both. What there is instead:
//
//  1. Reserve stock. This either takes every line or none, and is idempotent
//     under the caller's key.
//  2. Write the order and its event together, in one local transaction.
//  3. If step 2 fails, release the reservation.
//
// The window that remains is a crash between 1 and 2: the stock is taken and
// no order exists to justify it. It is recovered by a reaper that releases
// reservations with no matching order, which is not built here and is the
// first thing this design needs before it carries real money.
func (s *service) Place(ctx context.Context, in NewOrder) (Placement, error) {
	if err := validatePlacement(in); err != nil {
		return Placement{}, err
	}

	// A retried request must return the original order, not buy again.
	existing, err := s.repo.ByIdempotencyKey(ctx, in.IdempotencyKey)
	switch {
	case err == nil:
		return Placement{Order: existing, Replayed: true}, nil
	case !errors.Is(err, ErrOrderNotFound):
		return Placement{}, err
	}

	reserved, err := s.stock.Reserve(ctx, in.IdempotencyKey, toReserveLines(in.Lines))
	if err != nil {
		return Placement{}, err
	}

	assembled, err := s.assemble(in, reserved)
	if err != nil {
		// The basket was not orderable after all — mixed sellers, mixed
		// currencies — so put the stock back before answering.
		s.compensate(ctx, in.IdempotencyKey, in.Lines, err)
		return Placement{}, err
	}

	event, err := s.placedEvent(assembled)
	if err != nil {
		s.compensate(ctx, in.IdempotencyKey, in.Lines, err)
		return Placement{}, err
	}

	saved, err := s.repo.Save(ctx, assembled, []OutboxEvent{event})
	if err != nil {
		s.compensate(ctx, in.IdempotencyKey, in.Lines, err)
		return Placement{}, err
	}

	// Save resolves a lost race by returning the winner's order, so the ID
	// coming back may not be the one that went in.
	return Placement{Order: saved, Replayed: saved.ID != assembled.ID}, nil
}

// assemble turns what was reserved into an order, and refuses baskets that do
// not add up to one.
func (s *service) assemble(in NewOrder, reserved []ReservedLine) (Order, error) {
	if len(reserved) == 0 {
		return Order{}, &ValidationError{Fields: map[string]string{"lines": "nothing was reserved"}}
	}

	sellerID := reserved[0].SellerID
	currency := reserved[0].Currency
	lines := make([]Line, 0, len(reserved))

	for _, r := range reserved {
		if r.SellerID != sellerID {
			return Order{}, ErrMixedSellers
		}
		if r.Currency != currency {
			return Order{}, &ValidationError{Fields: map[string]string{
				"currency": "all items must be priced in one currency",
			}}
		}
		lines = append(lines, Line{
			ProductID:   r.ProductID,
			ProductName: r.ProductName,
			UnitMinor:   r.UnitMinor,
			Quantity:    r.Quantity,
		})
	}

	now := time.Now().UTC()
	return Order{
		ID:             s.repo.NextID(),
		BuyerUserID:    in.BuyerUserID,
		SellerID:       sellerID,
		IdempotencyKey: in.IdempotencyKey,
		Lines:          lines,
		TotalMinor:     Total(lines),
		Currency:       currency,
		Status:         StatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// compensate undoes a reservation whose order never happened.
//
// A failure here is logged and swallowed: the caller is already being told the
// placement failed, and turning a failed compensation into a second error would
// replace a clear message with a confusing one. What it does leave is stock
// held against nothing, which is the reaper's job.
func (s *service) compensate(ctx context.Context, key string, lines []NewLine, cause error) {
	if err := s.stock.Release(ctx, key, toReserveLines(lines)); err != nil {
		s.log.LogAttrs(ctx, slog.LevelError, "compensation failed; stock is held with no order",
			slog.String("idempotency_key", key),
			slog.String("cause", cause.Error()),
			slog.String("error", err.Error()))
	}
}

func (s *service) ByID(ctx context.Context, id string) (Order, error) {
	return s.repo.ByID(ctx, id)
}

func (s *service) ListForBuyer(ctx context.Context, buyerUserID string, limit, offset int) ([]Order, int, error) {
	limit, offset = ClampPage(limit, offset)
	return s.repo.ListForBuyer(ctx, buyerUserID, limit, offset)
}

// Cancel releases the reservation, then records the cancellation.
//
// The order of the two matters less than it looks: the status change is
// guarded on the order still being pending, so a second cancel finds nothing
// to move and releases nothing that was not already released.
func (s *service) Cancel(ctx context.Context, id, buyerUserID string) (Order, error) {
	found, err := s.authorize(ctx, id, buyerUserID)
	if err != nil {
		return Order{}, err
	}
	if found.Status != StatusPending {
		return Order{}, ErrNotPending
	}

	if err := s.stock.Release(ctx, found.IdempotencyKey, linesToReserve(found.Lines)); err != nil {
		return Order{}, fmt.Errorf("release stock: %w", err)
	}

	return s.transition(ctx, found, StatusPending, StatusCancelled, orderv1.EventOrderCancelled)
}

// MarkPaid stands in for a payment domain that does not exist yet. The
// reservation becomes a sale, and the stock stays taken.
func (s *service) MarkPaid(ctx context.Context, id, buyerUserID string) (Order, error) {
	found, err := s.authorize(ctx, id, buyerUserID)
	if err != nil {
		return Order{}, err
	}
	if found.Status != StatusPending {
		return Order{}, ErrNotPending
	}
	return s.transition(ctx, found, StatusPending, StatusPaid, orderv1.EventOrderPaid)
}

func (s *service) transition(ctx context.Context, o Order, from, to Status, eventType string) (Order, error) {
	moved := o
	moved.Status = to
	moved.UpdatedAt = time.Now().UTC()

	event, err := s.event(moved, eventType)
	if err != nil {
		return Order{}, err
	}

	// from is passed so the update is conditional: two concurrent cancels
	// cannot both succeed, and the second is told the order has moved on.
	return s.repo.UpdateStatus(ctx, o.ID, from, to, []OutboxEvent{event})
}

func (s *service) authorize(ctx context.Context, id, buyerUserID string) (Order, error) {
	found, err := s.repo.ByID(ctx, id)
	if err != nil {
		return Order{}, err
	}
	if found.BuyerUserID != buyerUserID {
		// Not "not found": the caller supplied a valid ID and is simply not
		// entitled to it. Pretending it does not exist would hide a real
		// authorization failure from anybody reading the logs.
		return Order{}, ErrNotBuyer
	}
	return found, nil
}

func (s *service) placedEvent(o Order) (OutboxEvent, error) {
	return s.event(o, orderv1.EventOrderPlaced)
}

func (s *service) event(o Order, eventType string) (OutboxEvent, error) {
	lines := make([]orderv1.OrderLine, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, orderv1.OrderLine{
			ProductID:   l.ProductID,
			ProductName: l.ProductName,
			UnitMinor:   l.UnitMinor,
			Quantity:    l.Quantity,
		})
	}

	payload, err := json.Marshal(orderv1.OrderEvent{
		Type:        eventType,
		OrderID:     o.ID,
		BuyerUserID: o.BuyerUserID,
		SellerID:    o.SellerID,
		Status:      string(o.Status),
		TotalMinor:  o.TotalMinor,
		Currency:    o.Currency,
		Lines:       lines,
		OccurredAt:  time.Now().UTC(),
	})
	if err != nil {
		return OutboxEvent{}, fmt.Errorf("marshal order event: %w", err)
	}

	// Keyed by seller, not by order: consumers build per-seller views, and a
	// seller's events arriving in order matters more than an order's do, since
	// an order's events are already causally separated by their status guard.
	return OutboxEvent{
		Topic:     orderv1.TopicOrderEvents,
		Key:       o.SellerID,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func validatePlacement(in NewOrder) error {
	fields := map[string]string{}

	if strings.TrimSpace(in.BuyerUserID) == "" {
		fields["buyer"] = "is required"
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		fields["idempotency_key"] = "is required; supply a unique value per order so a retry is safe"
	}

	switch {
	case len(in.Lines) == 0:
		fields["lines"] = "at least one item is required"
	case len(in.Lines) > MaxLines:
		fields["lines"] = fmt.Sprintf("at most %d items per order", MaxLines)
	}

	for _, l := range in.Lines {
		if strings.TrimSpace(l.ProductID) == "" {
			fields["lines"] = "every item needs a product_id"
		}
		if l.Quantity <= 0 {
			fields["lines"] = "every quantity must be greater than zero"
		}
	}
	return newValidationError(fields)
}

func toReserveLines(lines []NewLine) []ReserveLine {
	out := make([]ReserveLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, ReserveLine{ProductID: l.ProductID, Quantity: l.Quantity})
	}
	return out
}

func linesToReserve(lines []Line) []ReserveLine {
	out := make([]ReserveLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, ReserveLine{ProductID: l.ProductID, Quantity: l.Quantity})
	}
	return out
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
