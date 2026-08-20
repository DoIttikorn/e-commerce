package integration

import (
	"context"
	"testing"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/marketplace"
	marketplacemongo "github.com/DoIttikorn/e-commerce/internal/marketplace/mongodb"
)

func newMarketplaceRepo(t *testing.T) (*marketplacemongo.Repository, context.Context) {
	t.Helper()

	db, ctx := mongoFor(t, "marketplace")
	dropAll(t, ctx, db, marketplacemongo.CollectionName, marketplacemongo.SalesCollectionName)

	repo := marketplacemongo.NewRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	return repo, ctx
}

func listing(id, seller, name, description string, price int64, inStock bool) marketplace.Listing {
	return marketplace.Listing{
		ProductID: id, SellerID: seller, SellerName: "Shop " + seller,
		Name: name, Description: description,
		PriceMinor: price, Currency: "THB", InStock: inStock,
		UpdatedAt: time.Now().UTC(),
	}
}

func seedListings(t *testing.T, ctx context.Context, repo *marketplacemongo.Repository, ls ...marketplace.Listing) {
	t.Helper()
	for _, l := range ls {
		if err := repo.UpsertListing(ctx, l); err != nil {
			t.Fatalf("upsert %s: %v", l.ProductID, err)
		}
	}
}

func ids(ls []marketplace.Listing) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.ProductID)
	}
	return out
}

// The text index is the reason this projection exists, and it cannot be tested
// against a fake: relevance is the database's opinion, not the code's.
func TestSearchMatchesTextAndWeightsTheNameHigher(t *testing.T) {
	repo, ctx := newMarketplaceRepo(t)
	seedListings(t, ctx, repo,
		listing("p1", "s1", "Blue Ceramic Mug", "a plain cup", 25000, true),
		listing("p2", "s1", "Plain Cup", "made of ceramic, not a mug", 15000, true),
		listing("p3", "s1", "Wooden Spoon", "no relation", 5000, true),
	)

	found, total, err := repo.Search(ctx, marketplace.Query{
		Text: "mug", Sort: marketplace.SortRelevance, Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if total != 2 {
		t.Fatalf("total = %d, want 2 (the spoon should not match)", total)
	}
	// "Mug" in the title outranks "mug" in a description, which is what the
	// index weights are for.
	if found[0].ProductID != "p1" {
		t.Errorf("first result = %s, want p1 — the name should outweigh the description", found[0].ProductID)
	}
}

func TestSearchFiltersByPriceStockAndSeller(t *testing.T) {
	repo, ctx := newMarketplaceRepo(t)
	seedListings(t, ctx, repo,
		listing("cheap", "s1", "Cheap Mug", "", 5000, true),
		listing("mid", "s1", "Mid Mug", "", 25000, true),
		listing("dear", "s2", "Dear Mug", "", 90000, true),
		listing("gone", "s1", "Sold Out Mug", "", 20000, false),
	)

	t.Run("price range", func(t *testing.T) {
		found, _, err := repo.Search(ctx, marketplace.Query{
			MinPriceMinor: 10000, MaxPriceMinor: 30000, Sort: marketplace.SortPriceAsc, Limit: 10,
		})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		got := ids(found)
		if len(got) != 2 || got[0] != "gone" || got[1] != "mid" {
			t.Errorf("results = %v, want [gone mid] in ascending price", got)
		}
	})

	t.Run("in stock only", func(t *testing.T) {
		found, _, err := repo.Search(ctx, marketplace.Query{InStockOnly: true, Limit: 10})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		for _, l := range found {
			if l.ProductID == "gone" {
				t.Error("a sold-out listing was returned with in_stock=true")
			}
		}
	})

	t.Run("one seller", func(t *testing.T) {
		found, total, err := repo.Search(ctx, marketplace.Query{SellerID: "s2", Limit: 10})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		if total != 1 || found[0].ProductID != "dear" {
			t.Errorf("results = %v, want only s2's listing", ids(found))
		}
	})
}

// A seller event touches every listing that shop owns — the same fan-out the
// Product service does, arriving at a second consumer that never had to be
// told the first one existed.
func TestASellerRenameReachesEveryListing(t *testing.T) {
	repo, ctx := newMarketplaceRepo(t)
	seedListings(t, ctx, repo,
		listing("p1", "s1", "Mug", "", 100, true),
		listing("p2", "s1", "Plate", "", 200, true),
		listing("p3", "s2", "Spoon", "", 300, true),
	)

	touched, err := repo.ApplySellerChange(ctx, "s1", "Renamed Shop", true)
	if err != nil {
		t.Fatalf("ApplySellerChange() error = %v", err)
	}
	if touched != 2 {
		t.Errorf("touched %d listings, want 2", touched)
	}

	found, _, _ := repo.Search(ctx, marketplace.Query{SellerID: "s1", Limit: 10})
	for _, l := range found {
		if l.SellerName != "Renamed Shop" {
			t.Errorf("listing %s still shows %q", l.ProductID, l.SellerName)
		}
	}
}

// Suspension hides a shop without destroying the projection, so lifting it
// does not mean rebuilding from the event stream.
func TestSuspendingAShopHidesItsListingsWithoutDeletingThem(t *testing.T) {
	repo, ctx := newMarketplaceRepo(t)
	seedListings(t, ctx, repo, listing("p1", "s1", "Mug", "", 100, true))

	if _, err := repo.ApplySellerChange(ctx, "s1", "Shop s1", false); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, total, _ := repo.Search(ctx, marketplace.Query{Limit: 10}); total != 0 {
		t.Errorf("a suspended shop's listing is still visible")
	}

	if _, err := repo.ApplySellerChange(ctx, "s1", "Shop s1", true); err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	if _, total, _ := repo.Search(ctx, marketplace.Query{Limit: 10}); total != 1 {
		t.Errorf("the listing did not come back when the shop was reinstated")
	}
}

// Popularity is accumulated from order events, and at-least-once delivery means
// the same order will arrive again. A ranking that inflates on every redelivery
// is worse than no ranking at all.
func TestSalesAreCountedOncePerOrder(t *testing.T) {
	repo, ctx := newMarketplaceRepo(t)
	seedListings(t, ctx, repo,
		listing("popular", "s1", "Popular Mug", "", 100, true),
		listing("quiet", "s1", "Quiet Mug", "", 100, true),
	)

	sale := []marketplace.SoldLine{{ProductID: "popular", Quantity: 4}}
	for range 3 {
		if err := repo.RecordSale(ctx, "order-1", sale); err != nil {
			t.Fatalf("RecordSale() error = %v", err)
		}
	}
	if err := repo.RecordSale(ctx, "order-2", []marketplace.SoldLine{{ProductID: "quiet", Quantity: 1}}); err != nil {
		t.Fatalf("RecordSale() error = %v", err)
	}

	found, _, err := repo.Search(ctx, marketplace.Query{Sort: marketplace.SortBestSelling, Limit: 10})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}

	if found[0].ProductID != "popular" {
		t.Errorf("best seller = %s, want popular", found[0].ProductID)
	}
	if found[0].SoldCount != 4 {
		t.Errorf("sold count = %d, want 4 — the same order was counted more than once", found[0].SoldCount)
	}
}

// A product event must not resurrect a suspended shop, which is why
// seller_active is set on insert only.
func TestAProductUpdateDoesNotReinstateASuspendedShop(t *testing.T) {
	repo, ctx := newMarketplaceRepo(t)
	seedListings(t, ctx, repo, listing("p1", "s1", "Mug", "", 100, true))

	if _, err := repo.ApplySellerChange(ctx, "s1", "Shop s1", false); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	// The seller carries on editing their catalogue while suspended.
	seedListings(t, ctx, repo, listing("p1", "s1", "Mug, revised", "", 150, true))

	if _, total, _ := repo.Search(ctx, marketplace.Query{Limit: 10}); total != 0 {
		t.Error("a product update brought a suspended shop back into the results")
	}
}

// Pagination over a non-unique sort key needs a tiebreaker, or a page boundary
// can repeat one row and skip another.
func TestPagingByPriceDoesNotRepeatOrSkip(t *testing.T) {
	repo, ctx := newMarketplaceRepo(t)
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		seedListings(t, ctx, repo, listing(id, "s1", "Item "+id, "", 1000, true))
	}

	seen := map[string]bool{}
	for offset := 0; offset < 6; offset += 2 {
		page, _, err := repo.Search(ctx, marketplace.Query{
			Sort: marketplace.SortPriceAsc, Limit: 2, Offset: offset,
		})
		if err != nil {
			t.Fatalf("Search() error = %v", err)
		}
		for _, l := range page {
			if seen[l.ProductID] {
				t.Errorf("%s appeared on two pages", l.ProductID)
			}
			seen[l.ProductID] = true
		}
	}
	if len(seen) != 6 {
		t.Errorf("saw %d of 6 listings across the pages", len(seen))
	}
}
