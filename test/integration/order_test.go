package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	orderv1 "github.com/DoIttikorn/e-commerce/api/order/v1"
	productv1 "github.com/DoIttikorn/e-commerce/api/product/v1"
	"github.com/DoIttikorn/e-commerce/internal/kafka"
	"github.com/DoIttikorn/e-commerce/internal/order"
	"github.com/DoIttikorn/e-commerce/internal/order/grpcstock"
	ordermongo "github.com/DoIttikorn/e-commerce/internal/order/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/outbox"
	"github.com/DoIttikorn/e-commerce/internal/product"
	productgapi "github.com/DoIttikorn/e-commerce/internal/product/gapi"
	productmongo "github.com/DoIttikorn/e-commerce/internal/product/mongodb"
)

// This file wires the real thing: an Order service on its own database calling
// a real Product service over real gRPC, which takes stock out of a different
// database. Nothing between them is faked, because the interesting failures —
// a reservation that half-succeeds, a compensation that has to run — only
// appear when both sides are real.

type orderStack struct {
	svc         order.Service
	orderRepo   *ordermongo.Repository
	productRepo *productmongo.Repository
	orderDB     interface{ Name() string }
	ctx         context.Context
}

func startOrderStack(t *testing.T) (*orderStack, context.Context) {
	t.Helper()

	productDB, ctx := mongoFor(t, "product")
	dropAll(t, ctx, productDB, productmongo.CollectionName,
		productmongo.ReservationCollectionName, productmongo.DirectoryCollectionName)

	productRepo := productmongo.NewRepository(productDB)
	if err := productRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("product indexes: %v", err)
	}
	productSvc := product.NewService(productRepo, productmongo.NewDirectory(productDB), discard())

	// A real gRPC server on a real port, not an in-process shortcut: the
	// adapter's error translation only runs when a status actually crosses a
	// wire.
	grpcSrv := grpc.NewServer()
	productv1.RegisterStockServiceServer(grpcSrv, productgapi.New(productSvc))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = grpcSrv.Serve(listener) }()
	t.Cleanup(grpcSrv.Stop)

	stockClient, err := grpcstock.Dial(listener.Addr().String(), "", "")
	if err != nil {
		t.Fatalf("dial product: %v", err)
	}
	t.Cleanup(func() { _ = stockClient.Close() })

	orderDB, _ := mongoFor(t, "order")
	dropAll(t, ctx, orderDB, ordermongo.CollectionName, ordermongo.OutboxName)

	orderRepo := ordermongo.NewRepository(orderDB)
	if err := orderRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("order indexes: %v", err)
	}

	return &orderStack{
		svc:         order.NewService(orderRepo, stockClient, discard()),
		orderRepo:   orderRepo,
		productRepo: productRepo,
		orderDB:     orderDB,
		ctx:         ctx,
	}, ctx
}

func (s *orderStack) stock(t *testing.T, ctx context.Context, name string, qty int) product.Product {
	t.Helper()

	created, err := s.productRepo.Create(ctx, product.Product{
		ID:       s.productRepo.NextID(),
		SellerID: "seller-1", SellerName: "Order Shop", Name: name,
		PriceMinor: 25000, Currency: "THB", Stock: qty,
		CreatedAt: time.Now().UTC(),
	}, nil)
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return created
}

func TestPlacingAnOrderTakesStockFromTheProductService(t *testing.T) {
	stack, ctx := startOrderStack(t)
	item := stack.stock(t, ctx, "Mug", 10)

	placed, err := stack.svc.Place(ctx, order.NewOrder{
		BuyerUserID:    "buyer-1",
		IdempotencyKey: fmt.Sprintf("key-%d", time.Now().UnixNano()),
		Lines:          []order.NewLine{{ProductID: item.ID, Quantity: 3}},
	})
	if err != nil {
		t.Fatalf("Place() error = %v", err)
	}

	if placed.Order.TotalMinor != 75000 {
		t.Errorf("total = %d, want 75000", placed.Order.TotalMinor)
	}
	if placed.Order.SellerID != "seller-1" {
		t.Errorf("seller = %q, want it taken from the reservation", placed.Order.SellerID)
	}

	// The stock came out of the other service's database.
	after, err := stack.productRepo.ByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("ByID() error = %v", err)
	}
	if after.Stock != 7 {
		t.Errorf("stock = %d, want 7", after.Stock)
	}
}

// The transactional outbox: the order and its event are written together, so
// the event is already in the database before anything tries to publish it.
func TestPlacingAnOrderRecordsItsEventInTheOutbox(t *testing.T) {
	stack, ctx := startOrderStack(t)
	item := stack.stock(t, ctx, "Mug", 10)

	if _, err := stack.svc.Place(ctx, order.NewOrder{
		BuyerUserID:    "buyer-1",
		IdempotencyKey: fmt.Sprintf("key-%d", time.Now().UnixNano()),
		Lines:          []order.NewLine{{ProductID: item.ID, Quantity: 1}},
	}); err != nil {
		t.Fatalf("Place() error = %v", err)
	}

	db, _ := mongoFor(t, "order")
	pending, err := outbox.PendingCount(ctx, db.Collection(ordermongo.OutboxName))
	if err != nil {
		t.Fatalf("PendingCount() error = %v", err)
	}
	if pending != 1 {
		t.Errorf("pending outbox events = %d, want 1", pending)
	}
}

// The end of the saga that matters most: stock is taken, the order cannot be
// written, and the stock has to come back.
func TestAFailedOrderReleasesTheStockItTook(t *testing.T) {
	stack, ctx := startOrderStack(t)
	first := stack.stock(t, ctx, "First", 10)
	second := stack.stock(t, ctx, "Second", 10)

	// Two products, but they belong to the same seller, so force the failure
	// another way: ask for more of the second than exists.
	_, err := stack.svc.Place(ctx, order.NewOrder{
		BuyerUserID:    "buyer-1",
		IdempotencyKey: fmt.Sprintf("key-%d", time.Now().UnixNano()),
		Lines: []order.NewLine{
			{ProductID: first.ID, Quantity: 5},
			{ProductID: second.ID, Quantity: 999},
		},
	})
	if !errors.Is(err, order.ErrOutOfStock) {
		t.Fatalf("error = %v, want ErrOutOfStock", err)
	}

	// The reservation is all-or-nothing inside the Product service, so the
	// first line was never taken in the first place.
	after, _ := stack.productRepo.ByID(ctx, first.ID)
	if after.Stock != 10 {
		t.Errorf("stock = %d, want 10 — a failed basket took stock anyway", after.Stock)
	}
}

func TestPlacingTheSameOrderTwiceBuysOnce(t *testing.T) {
	stack, ctx := startOrderStack(t)
	item := stack.stock(t, ctx, "Mug", 10)

	in := order.NewOrder{
		BuyerUserID:    "buyer-1",
		IdempotencyKey: fmt.Sprintf("key-%d", time.Now().UnixNano()),
		Lines:          []order.NewLine{{ProductID: item.ID, Quantity: 2}},
	}

	first, err := stack.svc.Place(ctx, in)
	if err != nil {
		t.Fatalf("first Place() error = %v", err)
	}
	second, err := stack.svc.Place(ctx, in)
	if err != nil {
		t.Fatalf("retry Place() error = %v", err)
	}

	if second.Order.ID != first.Order.ID {
		t.Errorf("retry produced order %s, want the original %s", second.Order.ID, first.Order.ID)
	}
	if !second.Replayed {
		t.Error("the retry was not reported as a replay")
	}

	after, _ := stack.productRepo.ByID(ctx, item.ID)
	if after.Stock != 8 {
		t.Errorf("stock = %d, want 8 — the retry bought twice", after.Stock)
	}
}

func TestCancellingAnOrderPutsTheStockBack(t *testing.T) {
	stack, ctx := startOrderStack(t)
	item := stack.stock(t, ctx, "Mug", 10)

	placed, err := stack.svc.Place(ctx, order.NewOrder{
		BuyerUserID:    "buyer-1",
		IdempotencyKey: fmt.Sprintf("key-%d", time.Now().UnixNano()),
		Lines:          []order.NewLine{{ProductID: item.ID, Quantity: 4}},
	})
	if err != nil {
		t.Fatalf("Place() error = %v", err)
	}

	cancelled, err := stack.svc.Cancel(ctx, placed.Order.ID, "buyer-1")
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if cancelled.Status != order.StatusCancelled {
		t.Errorf("status = %q, want %q", cancelled.Status, order.StatusCancelled)
	}

	after, _ := stack.productRepo.ByID(ctx, item.ID)
	if after.Stock != 10 {
		t.Errorf("stock = %d, want 10 after the cancellation", after.Stock)
	}

	// Cancelling again must be refused rather than releasing the stock twice.
	if _, err := stack.svc.Cancel(ctx, placed.Order.ID, "buyer-1"); !errors.Is(err, order.ErrNotPending) {
		t.Errorf("second Cancel() error = %v, want ErrNotPending", err)
	}
	again, _ := stack.productRepo.ByID(ctx, item.ID)
	if again.Stock != 10 {
		t.Errorf("stock = %d after a double cancel, want 10", again.Stock)
	}
}

// The whole reason the reservation is atomic, exercised through two services
// and a network hop rather than against the repository directly.
func TestConcurrentOrdersCannotOversell(t *testing.T) {
	stack, ctx := startOrderStack(t)
	item := stack.stock(t, ctx, "Scarce", 5)

	const buyers = 20
	results := make(chan error, buyers)
	start := make(chan struct{})

	for i := range buyers {
		go func() {
			<-start
			_, err := stack.svc.Place(ctx, order.NewOrder{
				BuyerUserID:    fmt.Sprintf("buyer-%d", i),
				IdempotencyKey: fmt.Sprintf("concurrent-%d-%d", time.Now().UnixNano(), i),
				Lines:          []order.NewLine{{ProductID: item.ID, Quantity: 1}},
			})
			results <- err
		}()
	}
	close(start)

	var placed int
	for range buyers {
		if err := <-results; err == nil {
			placed++
		}
	}

	if placed != 5 {
		t.Errorf("%d orders were placed against 5 units, want exactly 5", placed)
	}

	after, _ := stack.productRepo.ByID(ctx, item.ID)
	if after.Stock != 0 {
		t.Errorf("stock = %d, want 0 — it must not go negative or strand a unit", after.Stock)
	}
}

// The relay is what turns outbox rows into messages. Untested, an order could
// be recorded perfectly and nobody downstream would ever hear about it.
func TestTheOutboxRelayPublishesAndMarksSent(t *testing.T) {
	stack, ctx := startOrderStack(t)
	brokers := kafkaBrokers(t)
	item := stack.stock(t, ctx, "Mug", 10)

	if err := kafka.EnsureTopic(ctx, brokers, orderv1.TopicOrderEvents, 1); err != nil {
		t.Fatalf("ensure topic: %v", err)
	}

	placed, err := stack.svc.Place(ctx, order.NewOrder{
		BuyerUserID:    "buyer-1",
		IdempotencyKey: fmt.Sprintf("relay-%d", time.Now().UnixNano()),
		Lines:          []order.NewLine{{ProductID: item.ID, Quantity: 2}},
	})
	if err != nil {
		t.Fatalf("Place() error = %v", err)
	}

	db, _ := mongoFor(t, "order")
	outboxColl := db.Collection(ordermongo.OutboxName)

	// A consumer subscribed before the relay runs, so the message is caught
	// rather than inferred from the outbox emptying.
	received := make(chan string, 4)
	consumer := kafka.NewConsumer(brokers, "itest-order", orderv1.TopicOrderEvents, discard())
	consumer.Handle(func(_ context.Context, _, value []byte) error {
		var event orderv1.OrderEvent
		if err := json.Unmarshal(value, &event); err != nil {
			return nil
		}
		if event.OrderID == placed.Order.ID {
			received <- event.Type
		}
		return nil
	})

	consumerCtx, stopConsumer := context.WithCancel(ctx)
	go consumer.Run(consumerCtx)
	t.Cleanup(func() { stopConsumer(); _ = consumer.Close() })

	relayCtx, stopRelay := context.WithCancel(ctx)
	go outbox.NewRelay(outboxColl, kafkaPublisher(t, brokers), discard()).Run(relayCtx)
	t.Cleanup(stopRelay)

	select {
	case got := <-received:
		if got != orderv1.EventOrderPlaced {
			t.Errorf("event type = %q, want %q", got, orderv1.EventOrderPlaced)
		}
	case <-time.After(30 * time.Second):
		pending, _ := outbox.PendingCount(ctx, outboxColl)
		t.Fatalf("the order event never arrived; %d still pending in the outbox", pending)
	}

	// And the row is marked, so the relay does not send it forever.
	waitFor(t, "the outbox row to be marked published", 15*time.Second, func() bool {
		pending, err := outbox.PendingCount(ctx, outboxColl)
		return err == nil && pending == 0
	})
}

func kafkaPublisher(t *testing.T, brokers []string) *kafka.Publisher {
	t.Helper()
	p := kafka.NewPublisher(brokers, discard())
	t.Cleanup(func() { _ = p.Close() })
	return p
}
