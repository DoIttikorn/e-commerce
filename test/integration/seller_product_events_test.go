package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/kafka"
	"github.com/DoIttikorn/e-commerce/internal/product"
	productevents "github.com/DoIttikorn/e-commerce/internal/product/events"
	productmongo "github.com/DoIttikorn/e-commerce/internal/product/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/seller"
	sellermongo "github.com/DoIttikorn/e-commerce/internal/seller/mongodb"

	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
)

// This file is the claim the whole event-driven design rests on: a change in
// one service reaches another without either calling the other, and without
// either sharing the other's database.
//
// Two real services, a real broker, two real databases. Nothing is faked.

func kafkaBrokers(t *testing.T) []string {
	t.Helper()

	if testing.Short() {
		t.Skip("needs Kafka; run make itest")
	}
	return []string{envOr("KAFKA_BROKERS", "127.0.0.1:29092")}
}

// waitFor polls until check passes or the deadline expires. Event delivery is
// asynchronous by definition, so a test that asserts immediately after the
// write is testing nothing and failing at random.
func waitFor(t *testing.T, what string, timeout time.Duration, check func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// startPipeline wires a real Seller service publishing to Kafka and a real
// Product service consuming from it, each on its own database.
func startPipeline(t *testing.T) (seller.Service, product.Service, *productmongo.Directory, context.Context) {
	t.Helper()

	brokers := kafkaBrokers(t)
	db, ctx := testMongo(t)

	// Separate databases, as in the compose stack: neither service can reach
	// the other's collections even by accident.
	sellerDB := db.Client().Database(db.Name() + "_seller_evt")
	productDB := db.Client().Database(db.Name() + "_product_evt")
	for _, d := range []string{sellermongo.CollectionName} {
		if err := sellerDB.Collection(d).Drop(ctx); err != nil {
			t.Fatalf("drop %s: %v", d, err)
		}
	}
	for _, d := range []string{productmongo.CollectionName, productmongo.DirectoryCollectionName} {
		if err := productDB.Collection(d).Drop(ctx); err != nil {
			t.Fatalf("drop %s: %v", d, err)
		}
	}

	// The topic must exist before the consumer subscribes, or the first run
	// against a fresh broker races auto-creation and times out.
	if err := kafka.EnsureTopic(ctx, brokers, sellerv1.TopicSellerEvents, 1); err != nil {
		t.Fatalf("ensure topic: %v", err)
	}

	sellerRepo := sellermongo.NewRepository(sellerDB)
	if err := sellerRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("seller indexes: %v", err)
	}

	publisher := kafka.NewPublisher(brokers, discard())
	t.Cleanup(func() { _ = publisher.Close() })
	sellerSvc := seller.NewService(sellerRepo, publisher, discard())

	productRepo := productmongo.NewRepository(productDB)
	if err := productRepo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("product indexes: %v", err)
	}
	directory := productmongo.NewDirectory(productDB)
	if err := directory.EnsureIndexes(ctx); err != nil {
		t.Fatalf("directory indexes: %v", err)
	}
	productSvc := product.NewService(productRepo, directory, discard())

	// One stable group, not a unique one per run.
	//
	// A fresh group each time replays the whole topic and leaves an abandoned
	// group behind, which piles up in the broker and clutters any tool pointed
	// at it. A stable group resumes from its committed offset instead, which is
	// all these tests need: every assertion is about a seller created inside the
	// test, and the consumer is running before that happens.
	const group = "itest-product"
	consumer := kafka.NewConsumer(brokers, group, sellerv1.TopicSellerEvents, discard())
	consumer.Handle(productevents.SellerHandler(productSvc, discard()))

	consumerCtx, stopConsumer := context.WithCancel(ctx)
	go consumer.Run(consumerCtx)
	t.Cleanup(func() {
		stopConsumer()
		_ = consumer.Close()
	})

	return sellerSvc, productSvc, directory, ctx
}

func TestSellerEventReachesProductWithoutACall(t *testing.T) {
	sellerSvc, productSvc, directory, ctx := startPipeline(t)

	shopName := fmt.Sprintf("Test Shop %d", time.Now().UnixNano())
	owner := fmt.Sprintf("owner-%d", time.Now().UnixNano())

	created, err := sellerSvc.Register(ctx, seller.NewSeller{UserID: owner, ShopName: shopName})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// The Product service learns the shop exists purely from the event stream.
	waitFor(t, "the seller to appear in the product directory", 30*time.Second, func() bool {
		ref, err := directory.Get(ctx, created.ID)
		return err == nil && ref.ShopName == shopName
	})

	// And can now stamp its name onto a product without asking anybody.
	listed, err := productSvc.Create(ctx, product.NewProduct{
		OwnerUserID: owner, Name: "Blue Mug", PriceMinor: 25000, Currency: "THB", Stock: 3,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if listed.SellerName != shopName {
		t.Errorf("product shop name = %q, want %q", listed.SellerName, shopName)
	}
	if listed.SellerID != created.ID {
		t.Errorf("product seller id = %q, want %q", listed.SellerID, created.ID)
	}
}

// The reason the copy is safe to hold: when the original changes, the copy is
// corrected, and no read path ever had to make a call to find that out.
func TestRenamingAShopUpdatesProductsAlreadyListed(t *testing.T) {
	sellerSvc, productSvc, directory, ctx := startPipeline(t)

	stamp := time.Now().UnixNano()
	owner := fmt.Sprintf("owner-%d", stamp)
	original := fmt.Sprintf("Original Shop %d", stamp)

	created, err := sellerSvc.Register(ctx, seller.NewSeller{UserID: owner, ShopName: original})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	waitFor(t, "the seller to reach the product directory", 30*time.Second, func() bool {
		_, err := directory.Get(ctx, created.ID)
		return err == nil
	})

	listed, err := productSvc.Create(ctx, product.NewProduct{
		OwnerUserID: owner, Name: "Blue Mug", PriceMinor: 25000, Currency: "THB", Stock: 3,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	renamed := fmt.Sprintf("Renamed Shop %d", stamp)
	if _, err := sellerSvc.Update(ctx, created.ID, seller.Update{ShopName: &renamed}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	// The already-listed product picks up the new name, with no call between
	// the two services and no shared collection.
	waitFor(t, "the product to carry the new shop name", 30*time.Second, func() bool {
		got, err := productSvc.ByID(ctx, listed.ID)
		return err == nil && got.SellerName == renamed
	})
}

// A suspension is the same mechanism carrying a different fact, which is the
// point of one topic per domain rather than one per event type.
func TestSuspendingAShopReachesTheProductDirectory(t *testing.T) {
	sellerSvc, _, directory, ctx := startPipeline(t)

	stamp := time.Now().UnixNano()
	created, err := sellerSvc.Register(ctx, seller.NewSeller{
		UserID:   fmt.Sprintf("owner-%d", stamp),
		ShopName: fmt.Sprintf("Suspendable Shop %d", stamp),
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	waitFor(t, "the seller to reach the product directory", 30*time.Second, func() bool {
		_, err := directory.Get(ctx, created.ID)
		return err == nil
	})

	suspended := seller.StatusSuspended
	if _, err := sellerSvc.Update(ctx, created.ID, seller.Update{Status: &suspended}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	waitFor(t, "the suspension to reach the product directory", 30*time.Second, func() bool {
		ref, err := directory.Get(ctx, created.ID)
		return err == nil && ref.Status == string(seller.StatusSuspended)
	})
}
