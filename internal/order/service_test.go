package order

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

type fakeRepo struct {
	saveFn    func(context.Context, Order, []OutboxEvent) (Order, error)
	byKeyFn   func(context.Context, string) (Order, error)
	byIDFn    func(context.Context, string) (Order, error)
	updateFn  func(context.Context, string, Status, Status, []OutboxEvent) (Order, error)
	nextIDVal string

	saved       Order
	savedEvents []OutboxEvent
}

func (f *fakeRepo) NextID() string {
	if f.nextIDVal != "" {
		return f.nextIDVal
	}
	return "order-1"
}

func (f *fakeRepo) Save(ctx context.Context, o Order, events []OutboxEvent) (Order, error) {
	f.saved, f.savedEvents = o, events
	if f.saveFn != nil {
		return f.saveFn(ctx, o, events)
	}
	return o, nil
}

func (f *fakeRepo) ByIdempotencyKey(ctx context.Context, key string) (Order, error) {
	if f.byKeyFn != nil {
		return f.byKeyFn(ctx, key)
	}
	return Order{}, ErrOrderNotFound
}

func (f *fakeRepo) ByID(ctx context.Context, id string) (Order, error) {
	if f.byIDFn != nil {
		return f.byIDFn(ctx, id)
	}
	return Order{
		ID: id, BuyerUserID: "buyer", SellerID: "seller-1", IdempotencyKey: "key-1",
		Status: StatusPending, Lines: []Line{{ProductID: "p1", UnitMinor: 1000, Quantity: 2}},
	}, nil
}

func (f *fakeRepo) ListForBuyer(context.Context, string, int, int) ([]Order, int, error) {
	return []Order{{ID: "order-1"}}, 1, nil
}

func (f *fakeRepo) UpdateStatus(ctx context.Context, id string, from, to Status, events []OutboxEvent) (Order, error) {
	f.savedEvents = events
	if f.updateFn != nil {
		return f.updateFn(ctx, id, from, to, events)
	}
	return Order{ID: id, Status: to}, nil
}

type fakeStock struct {
	reserveFn func(context.Context, string, []ReserveLine) ([]ReservedLine, error)
	releaseFn func(context.Context, string, []ReserveLine) error

	reserveCalls int
	releasedKeys []string
}

func (f *fakeStock) Reserve(ctx context.Context, key string, items []ReserveLine) ([]ReservedLine, error) {
	f.reserveCalls++
	if f.reserveFn != nil {
		return f.reserveFn(ctx, key, items)
	}
	out := make([]ReservedLine, 0, len(items))
	for _, i := range items {
		out = append(out, ReservedLine{
			ProductID: i.ProductID, ProductName: "Mug", SellerID: "seller-1",
			UnitMinor: 25000, Currency: "THB", Quantity: i.Quantity,
		})
	}
	return out, nil
}

func (f *fakeStock) Release(ctx context.Context, key string, items []ReserveLine) error {
	f.releasedKeys = append(f.releasedKeys, key)
	if f.releaseFn != nil {
		return f.releaseFn(ctx, key, items)
	}
	return nil
}

func newTestService(repo *fakeRepo, stock *fakeStock) Service {
	return NewService(repo, stock, slog.New(slog.DiscardHandler))
}

func validOrder() NewOrder {
	return NewOrder{
		BuyerUserID:    "buyer",
		IdempotencyKey: "key-1",
		Lines:          []NewLine{{ProductID: "p1", Quantity: 2}},
	}
}

func TestPlaceSnapshotsPriceAndName(t *testing.T) {
	repo, stock := &fakeRepo{}, &fakeStock{}

	placed, err := newTestService(repo, stock).Place(context.Background(), validOrder())
	if err != nil {
		t.Fatalf("Place() error = %v", err)
	}

	if placed.Replayed {
		t.Error("a first placement was reported as a replay")
	}
	// The snapshot is the whole point: a reprice tomorrow must not change what
	// was agreed today.
	line := repo.saved.Lines[0]
	if line.UnitMinor != 25000 || line.ProductName != "Mug" {
		t.Errorf("line = %+v, want the price and name from the reservation", line)
	}
	if repo.saved.TotalMinor != 50000 {
		t.Errorf("total = %d, want 50000", repo.saved.TotalMinor)
	}
	if repo.saved.Status != StatusPending {
		t.Errorf("status = %q, want %q", repo.saved.Status, StatusPending)
	}
	// The order and its event are handed to the repository together, which is
	// what lets it write them in one transaction.
	if len(repo.savedEvents) != 1 {
		t.Errorf("saved %d events with the order, want 1", len(repo.savedEvents))
	}
}

// A retried request must return the original order and reserve nothing.
func TestPlaceIsIdempotent(t *testing.T) {
	repo := &fakeRepo{byKeyFn: func(context.Context, string) (Order, error) {
		return Order{ID: "order-1", Status: StatusPending, TotalMinor: 50000}, nil
	}}
	stock := &fakeStock{}

	placed, err := newTestService(repo, stock).Place(context.Background(), validOrder())
	if err != nil {
		t.Fatalf("Place() error = %v", err)
	}

	if !placed.Replayed {
		t.Error("a replayed key was not reported as a replay")
	}
	if stock.reserveCalls != 0 {
		t.Errorf("Reserve called %d times on a replay, want 0", stock.reserveCalls)
	}
}

func TestPlaceSurfacesOutOfStock(t *testing.T) {
	stock := &fakeStock{reserveFn: func(context.Context, string, []ReserveLine) ([]ReservedLine, error) {
		return nil, ErrOutOfStock
	}}
	repo := &fakeRepo{}

	_, err := newTestService(repo, stock).Place(context.Background(), validOrder())

	if !errors.Is(err, ErrOutOfStock) {
		t.Errorf("error = %v, want ErrOutOfStock", err)
	}
	if repo.saved.ID != "" {
		t.Error("an order was saved despite the reservation failing")
	}
	// Nothing was taken, so nothing may be released.
	if len(stock.releasedKeys) != 0 {
		t.Errorf("released %v after a failed reservation, want nothing", stock.releasedKeys)
	}
}

// The saga's compensating step: stock was taken, the order was not written, so
// the stock has to go back.
func TestPlaceReleasesStockWhenTheOrderCannotBeSaved(t *testing.T) {
	repo := &fakeRepo{saveFn: func(context.Context, Order, []OutboxEvent) (Order, error) {
		return Order{}, errors.New("mongo unavailable")
	}}
	stock := &fakeStock{}

	_, err := newTestService(repo, stock).Place(context.Background(), validOrder())

	if err == nil {
		t.Fatal("Place() error = nil, want the save failure")
	}
	if len(stock.releasedKeys) != 1 || stock.releasedKeys[0] != "key-1" {
		t.Errorf("released %v, want the reservation to be compensated under key-1", stock.releasedKeys)
	}
}

// A basket spanning two shops cannot become one order, and the stock reserved
// for it must not be stranded.
func TestPlaceRejectsAndCompensatesMixedSellers(t *testing.T) {
	stock := &fakeStock{reserveFn: func(_ context.Context, _ string, items []ReserveLine) ([]ReservedLine, error) {
		return []ReservedLine{
			{ProductID: "p1", SellerID: "seller-1", UnitMinor: 100, Currency: "THB", Quantity: 1},
			{ProductID: "p2", SellerID: "seller-2", UnitMinor: 100, Currency: "THB", Quantity: 1},
		}, nil
	}}
	repo := &fakeRepo{}

	in := validOrder()
	in.Lines = append(in.Lines, NewLine{ProductID: "p2", Quantity: 1})

	_, err := newTestService(repo, stock).Place(context.Background(), in)

	if !errors.Is(err, ErrMixedSellers) {
		t.Fatalf("error = %v, want ErrMixedSellers", err)
	}
	if len(stock.releasedKeys) != 1 {
		t.Errorf("released %v, want the reservation compensated", stock.releasedKeys)
	}
	if repo.saved.ID != "" {
		t.Error("an order was saved for a mixed-seller basket")
	}
}

// A failed compensation must not turn into a second error for the caller: the
// placement failed either way, and two errors describe it worse than one.
func TestPlaceStillReportsTheOriginalFailureWhenCompensationFails(t *testing.T) {
	repo := &fakeRepo{saveFn: func(context.Context, Order, []OutboxEvent) (Order, error) {
		return Order{}, errors.New("mongo unavailable")
	}}
	stock := &fakeStock{releaseFn: func(context.Context, string, []ReserveLine) error {
		return errors.New("product service unreachable")
	}}

	_, err := newTestService(repo, stock).Place(context.Background(), validOrder())

	if err == nil || err.Error() != "mongo unavailable" {
		t.Errorf("error = %v, want the original save failure", err)
	}
}

func TestPlaceRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*NewOrder)
		wantField string
	}{
		{"no idempotency key", func(o *NewOrder) { o.IdempotencyKey = "" }, "idempotency_key"},
		{"no buyer", func(o *NewOrder) { o.BuyerUserID = "" }, "buyer"},
		{"no lines", func(o *NewOrder) { o.Lines = nil }, "lines"},
		{"zero quantity", func(o *NewOrder) { o.Lines[0].Quantity = 0 }, "lines"},
		{"blank product", func(o *NewOrder) { o.Lines[0].ProductID = " " }, "lines"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validOrder()
			in.Lines = append([]NewLine{}, in.Lines...)
			tt.mutate(&in)
			stock := &fakeStock{}

			_, err := newTestService(&fakeRepo{}, stock).Place(context.Background(), in)

			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("error = %v, want a *ValidationError", err)
			}
			if _, ok := verr.Fields[tt.wantField]; !ok {
				t.Errorf("fields = %v, want an entry for %q", verr.Fields, tt.wantField)
			}
			// Nothing may be reserved for a request that was never valid.
			if stock.reserveCalls != 0 {
				t.Errorf("Reserve was called %d times for invalid input", stock.reserveCalls)
			}
		})
	}
}

func TestCancelReleasesStockAndClosesTheOrder(t *testing.T) {
	repo, stock := &fakeRepo{}, &fakeStock{}

	cancelled, err := newTestService(repo, stock).Cancel(context.Background(), "order-1", "buyer")
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	if cancelled.Status != StatusCancelled {
		t.Errorf("status = %q, want %q", cancelled.Status, StatusCancelled)
	}
	if len(stock.releasedKeys) != 1 || stock.releasedKeys[0] != "key-1" {
		t.Errorf("released %v, want the order's reservation key", stock.releasedKeys)
	}
}

func TestCancelRefusesSomebodyElsesOrder(t *testing.T) {
	stock := &fakeStock{}

	_, err := newTestService(&fakeRepo{}, stock).Cancel(context.Background(), "order-1", "another-buyer")

	if !errors.Is(err, ErrNotBuyer) {
		t.Errorf("error = %v, want ErrNotBuyer", err)
	}
	if len(stock.releasedKeys) != 0 {
		t.Error("stock was released for an order the caller does not own")
	}
}

func TestCancelRefusesAnOrderThatHasMovedOn(t *testing.T) {
	repo := &fakeRepo{byIDFn: func(_ context.Context, id string) (Order, error) {
		return Order{ID: id, BuyerUserID: "buyer", Status: StatusPaid}, nil
	}}
	stock := &fakeStock{}

	_, err := newTestService(repo, stock).Cancel(context.Background(), "order-1", "buyer")

	if !errors.Is(err, ErrNotPending) {
		t.Errorf("error = %v, want ErrNotPending", err)
	}
	if len(stock.releasedKeys) != 0 {
		t.Error("stock was released for a paid order")
	}
}

func TestMarkPaidKeepsTheStock(t *testing.T) {
	repo, stock := &fakeRepo{}, &fakeStock{}

	paid, err := newTestService(repo, stock).MarkPaid(context.Background(), "order-1", "buyer")
	if err != nil {
		t.Fatalf("MarkPaid() error = %v", err)
	}

	if paid.Status != StatusPaid {
		t.Errorf("status = %q, want %q", paid.Status, StatusPaid)
	}
	if len(stock.releasedKeys) != 0 {
		t.Error("paying an order released its stock")
	}
}

func TestLineSubtotal(t *testing.T) {
	l := Line{UnitMinor: 25000, Quantity: 3}
	if got := l.Subtotal(); got != 75000 {
		t.Errorf("Subtotal() = %d, want 75000", got)
	}
	if got := Total([]Line{l, {UnitMinor: 100, Quantity: 2}}); got != 75200 {
		t.Errorf("Total() = %d, want 75200", got)
	}
}

func TestClampPage(t *testing.T) {
	if l, o := ClampPage(0, -5); l != DefaultPageSize || o != 0 {
		t.Errorf("ClampPage(0,-5) = %d,%d; want %d,0", l, o, DefaultPageSize)
	}
	if l, _ := ClampPage(MaxPageSize+100, 0); l != MaxPageSize {
		t.Errorf("limit = %d, want it clamped to %d", l, MaxPageSize)
	}
}
