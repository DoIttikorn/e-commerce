package integration

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/product"
	productmongo "github.com/DoIttikorn/e-commerce/internal/product/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/product/rediscache"
)

func testMongo(t *testing.T) (*mongo.Database, context.Context) {
	t.Helper()

	if testing.Short() {
		t.Skip("needs MongoDB; run make itest")
	}

	uri := envOr("MONGO_URI", "mongodb://127.0.0.1:27017")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("mongo unreachable at %s: %v", uri, err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	return client.Database(envOr("MONGO_DATABASE", "ecommerce_test")), ctx
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// newCachedRepo returns the plain repository and the same repository behind the
// Redis decorator, so a test can change the database behind the cache's back.
func newCachedRepo(t *testing.T) (plain *productmongo.Repository, cached product.Repository, ctx context.Context) {
	t.Helper()

	db, ctx := testMongo(t)
	if err := db.Collection(productmongo.CollectionName).Drop(ctx); err != nil {
		t.Fatalf("drop products: %v", err)
	}

	plain = productmongo.NewRepository(db)
	if err := plain.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}

	rdb, err := database.NewRedis(ctx, envOr("REDIS_ADDR", "127.0.0.1:6379"), "", 0)
	if err != nil {
		t.Fatalf("redis unreachable: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	if err := rdb.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}

	cached = rediscache.New(plain, rdb, time.Minute, discard(), prometheus.NewRegistry())
	return plain, cached, ctx
}

func sampleProduct(name string) product.Product {
	return product.Product{
		SellerID: "seller-1", SellerName: "Original Shop", Name: name,
		PriceMinor: 25000, Currency: "THB", Stock: 5,
		CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
}

// The cache is only worth having if it actually serves reads. This proves it
// does, by changing the database underneath and watching the old value come
// back — which no amount of counting cache hits can demonstrate as directly.
func TestCacheServesTheSecondRead(t *testing.T) {
	plain, cached, ctx := newCachedRepo(t)

	created, err := cached.Create(ctx, sampleProduct("Blue Mug"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// First read populates the cache.
	if _, err := cached.ByID(ctx, created.ID); err != nil {
		t.Fatalf("first ByID() error = %v", err)
	}

	// Change the database behind the cache's back.
	newName := "Renamed In The Database"
	if _, err := plain.Update(ctx, created.ID, product.Update{Name: &newName}); err != nil {
		t.Fatalf("bypassing Update() error = %v", err)
	}

	got, err := cached.ByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("second ByID() error = %v", err)
	}
	if got.Name != "Blue Mug" {
		t.Errorf("name = %q, want the cached %q — the cache did not serve this read",
			got.Name, "Blue Mug")
	}
}

// Writing through the cache must invalidate it, or the stale value above would
// be permanent rather than a deliberate demonstration.
func TestWritesInvalidateTheCache(t *testing.T) {
	_, cached, ctx := newCachedRepo(t)

	created, err := cached.Create(ctx, sampleProduct("Blue Mug"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := cached.ByID(ctx, created.ID); err != nil {
		t.Fatalf("priming ByID() error = %v", err)
	}

	newName := "Green Mug"
	if _, err := cached.Update(ctx, created.ID, product.Update{Name: &newName}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err := cached.ByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("ByID() after update error = %v", err)
	}
	if got.Name != newName {
		t.Errorf("name = %q, want the updated %q", got.Name, newName)
	}
}

// A seller rename touches many products at once. Each one must be dropped from
// the cache, which is why the repository returns the affected IDs rather than
// leaving the cache to guess which keys are involved.
func TestRenameSellerInvalidatesEveryAffectedProduct(t *testing.T) {
	_, cached, ctx := newCachedRepo(t)

	var ids []string
	for _, name := range []string{"Mug", "Plate", "Bowl"} {
		created, err := cached.Create(ctx, sampleProduct(name))
		if err != nil {
			t.Fatalf("Create(%s) error = %v", name, err)
		}
		ids = append(ids, created.ID)

		if _, err := cached.ByID(ctx, created.ID); err != nil {
			t.Fatalf("priming ByID() error = %v", err)
		}
	}

	affected, err := cached.RenameSeller(ctx, "seller-1", "Brand New Shop")
	if err != nil {
		t.Fatalf("RenameSeller() error = %v", err)
	}
	if len(affected) != len(ids) {
		t.Fatalf("affected %d products, want %d", len(affected), len(ids))
	}

	for _, id := range ids {
		got, err := cached.ByID(ctx, id)
		if err != nil {
			t.Fatalf("ByID(%s) error = %v", id, err)
		}
		if got.SellerName != "Brand New Shop" {
			t.Errorf("product %s still shows %q — its cache entry was not dropped",
				id, got.SellerName)
		}
	}
}

// Applying the same rename twice must cost no writes the second time, because
// at-least-once delivery makes repeats a certainty rather than a possibility.
func TestRenameSellerIsANoOpWhenNothingChanges(t *testing.T) {
	_, cached, ctx := newCachedRepo(t)

	if _, err := cached.Create(ctx, sampleProduct("Mug")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if _, err := cached.RenameSeller(ctx, "seller-1", "New Shop"); err != nil {
		t.Fatalf("first RenameSeller() error = %v", err)
	}

	affected, err := cached.RenameSeller(ctx, "seller-1", "New Shop")
	if err != nil {
		t.Fatalf("second RenameSeller() error = %v", err)
	}
	if len(affected) != 0 {
		t.Errorf("second rename touched %d products, want 0", len(affected))
	}
}

// Deleting through the cache must not leave the entry behind.
func TestDeleteInvalidatesTheCache(t *testing.T) {
	_, cached, ctx := newCachedRepo(t)

	created, err := cached.Create(ctx, sampleProduct("Mug"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := cached.ByID(ctx, created.ID); err != nil {
		t.Fatalf("priming ByID() error = %v", err)
	}

	if err := cached.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if _, err := cached.ByID(ctx, created.ID); !errors.Is(err, product.ErrProductNotFound) {
		t.Errorf("error = %v, want ErrProductNotFound — a deleted product was served from cache", err)
	}
}
