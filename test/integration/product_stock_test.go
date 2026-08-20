package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/product"
	productmongo "github.com/DoIttikorn/e-commerce/internal/product/mongodb"
)

func newStockRepo(t *testing.T) (*productmongo.Repository, context.Context) {
	t.Helper()

	db, ctx := mongoFor(t, "product")
	dropAll(t, ctx, db, productmongo.CollectionName, productmongo.ReservationCollectionName)

	repo := productmongo.NewRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	return repo, ctx
}

func seedStock(t *testing.T, ctx context.Context, repo *productmongo.Repository, name string, stock int) product.Product {
	t.Helper()

	created, err := repo.Create(ctx, product.Product{
		ID:       repo.NextID(),
		SellerID: "seller-1", SellerName: "Stock Shop", Name: name,
		PriceMinor: 25000, Currency: "THB", Stock: stock,
		CreatedAt: time.Now().UTC(),
	}, nil)
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return created
}

func TestReserveTakesStock(t *testing.T) {
	repo, ctx := newStockRepo(t)
	item := seedStock(t, ctx, repo, "Mug", 10)

	reserved, err := repo.Reserve(ctx, "order-1", []product.ReserveItem{{ProductID: item.ID, Quantity: 3}})
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}

	if len(reserved) != 1 || reserved[0].Quantity != 3 {
		t.Fatalf("reserved = %+v, want one line of 3", reserved)
	}
	// The snapshot is the point: an order must not change when a product is
	// repriced tomorrow.
	if reserved[0].UnitMinor != 25000 || reserved[0].ProductName != "Mug" {
		t.Errorf("snapshot = %+v, want the price and name at reservation time", reserved[0])
	}

	after, err := repo.ByID(ctx, item.ID)
	if err != nil {
		t.Fatalf("ByID() error = %v", err)
	}
	if after.Stock != 7 {
		t.Errorf("stock = %d, want 7", after.Stock)
	}
}

// The test this whole design exists for. Twenty buyers, one unit.
func TestOnlyOneBuyerGetsTheLastUnit(t *testing.T) {
	repo, ctx := newStockRepo(t)
	item := seedStock(t, ctx, repo, "Last One", 1)

	const buyers = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, buyers)

	for i := range buyers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // line them up so the attempts genuinely overlap
			_, err := repo.Reserve(ctx,
				fmt.Sprintf("order-%d", i),
				[]product.ReserveItem{{ProductID: item.ID, Quantity: 1}})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var won, lost int
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, product.ErrInsufficientStock):
			lost++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if won != 1 {
		t.Errorf("%d buyers got the last unit, want exactly 1", won)
	}
	if lost != buyers-1 {
		t.Errorf("%d buyers were refused, want %d", lost, buyers-1)
	}

	after, _ := repo.ByID(ctx, item.ID)
	if after.Stock != 0 {
		t.Errorf("stock = %d, want 0 — it must never go negative or strand a unit", after.Stock)
	}
}

// A timed-out caller retries, and cannot tell whether the first attempt landed.
// Reserving twice under one key must take stock once.
func TestReserveIsIdempotent(t *testing.T) {
	repo, ctx := newStockRepo(t)
	item := seedStock(t, ctx, repo, "Mug", 10)

	req := []product.ReserveItem{{ProductID: item.ID, Quantity: 4}}
	first, err := repo.Reserve(ctx, "order-retry", req)
	if err != nil {
		t.Fatalf("first Reserve() error = %v", err)
	}

	second, err := repo.Reserve(ctx, "order-retry", req)
	if err != nil {
		t.Fatalf("retry Reserve() error = %v", err)
	}

	if len(second) != len(first) || second[0].Quantity != first[0].Quantity {
		t.Errorf("retry returned %+v, want the same answer as %+v", second, first)
	}

	after, _ := repo.ByID(ctx, item.ID)
	if after.Stock != 6 {
		t.Errorf("stock = %d, want 6 — the retry took stock twice", after.Stock)
	}
}

// All or nothing: a basket whose second line cannot be satisfied must leave the
// first line untouched.
func TestReserveIsAllOrNothing(t *testing.T) {
	repo, ctx := newStockRepo(t)
	plenty := seedStock(t, ctx, repo, "Plenty", 100)
	scarce := seedStock(t, ctx, repo, "Scarce", 1)

	_, err := repo.Reserve(ctx, "order-basket", []product.ReserveItem{
		{ProductID: plenty.ID, Quantity: 5},
		{ProductID: scarce.ID, Quantity: 9},
	})

	if !errors.Is(err, product.ErrInsufficientStock) {
		t.Fatalf("error = %v, want ErrInsufficientStock", err)
	}

	after, _ := repo.ByID(ctx, plenty.ID)
	if after.Stock != 100 {
		t.Errorf("the first line was decremented to %d despite the basket failing", after.Stock)
	}
}

func TestReserveRejectsUnknownProduct(t *testing.T) {
	repo, ctx := newStockRepo(t)

	_, err := repo.Reserve(ctx, "order-missing",
		[]product.ReserveItem{{ProductID: "000000000000000000000000", Quantity: 1}})

	if !errors.Is(err, product.ErrProductNotFound) {
		t.Errorf("error = %v, want ErrProductNotFound — distinct from insufficient stock", err)
	}
}

func TestReleasePutsStockBack(t *testing.T) {
	repo, ctx := newStockRepo(t)
	item := seedStock(t, ctx, repo, "Mug", 10)
	req := []product.ReserveItem{{ProductID: item.ID, Quantity: 4}}

	if _, err := repo.Reserve(ctx, "order-cancel", req); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := repo.Release(ctx, "order-cancel", req); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	after, _ := repo.ByID(ctx, item.ID)
	if after.Stock != 10 {
		t.Errorf("stock = %d, want 10 after the release", after.Stock)
	}
}

// Compensation runs on the unhappy path, which is exactly where retries happen.
// Releasing twice, or releasing something never reserved, must not invent stock.
func TestReleaseIsIdempotentAndForgiving(t *testing.T) {
	repo, ctx := newStockRepo(t)
	item := seedStock(t, ctx, repo, "Mug", 10)
	req := []product.ReserveItem{{ProductID: item.ID, Quantity: 4}}

	if _, err := repo.Reserve(ctx, "order-x", req); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	for range 3 {
		if err := repo.Release(ctx, "order-x", req); err != nil {
			t.Fatalf("Release() error = %v", err)
		}
	}
	if err := repo.Release(ctx, "never-reserved", req); err != nil {
		t.Errorf("releasing an unknown key returned %v, want nil", err)
	}

	after, _ := repo.ByID(ctx, item.ID)
	if after.Stock != 10 {
		t.Errorf("stock = %d, want 10 — repeated releases conjured stock", after.Stock)
	}
}
