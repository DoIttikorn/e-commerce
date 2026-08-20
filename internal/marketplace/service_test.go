package marketplace

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type fakeRepo struct {
	searchFn func(context.Context, Query) ([]Listing, int, error)

	gotQuery    Query
	upserted    []Listing
	removed     []string
	sellerCalls int
	sales       []string
}

func (f *fakeRepo) Search(ctx context.Context, q Query) ([]Listing, int, error) {
	f.gotQuery = q
	if f.searchFn != nil {
		return f.searchFn(ctx, q)
	}
	return []Listing{{ProductID: "p1"}}, 1, nil
}

func (f *fakeRepo) UpsertListing(_ context.Context, l Listing) error {
	f.upserted = append(f.upserted, l)
	return nil
}

func (f *fakeRepo) RemoveListing(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeRepo) ApplySellerChange(context.Context, string, string, bool) (int64, error) {
	f.sellerCalls++
	return 3, nil
}

func (f *fakeRepo) RecordSale(_ context.Context, orderID string, _ []SoldLine) error {
	f.sales = append(f.sales, orderID)
	return nil
}

func newTestService(repo *fakeRepo) Service {
	return NewService(repo, slog.New(slog.DiscardHandler))
}

// A search box is not a form: nonsense is clamped rather than refused, so a
// hand-edited URL still returns something useful.
func TestSearchClampsRatherThanRefuses(t *testing.T) {
	tests := []struct {
		name     string
		in       Query
		wantSort Sort
		wantLim  int
	}{
		{"empty query browses newest", Query{}, SortNewest, DefaultPageSize},
		{"unknown sort falls back", Query{Sort: Sort("whatever")}, SortNewest, DefaultPageSize},
		{"relevance without text is meaningless", Query{Sort: SortRelevance}, SortNewest, DefaultPageSize},
		{"relevance with text is kept", Query{Sort: SortRelevance, Text: "mug"}, SortRelevance, DefaultPageSize},
		{"limit is capped", Query{Limit: MaxPageSize + 500}, SortNewest, MaxPageSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeRepo{}

			if _, _, err := newTestService(repo).Search(context.Background(), tt.in); err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			if repo.gotQuery.Sort != tt.wantSort {
				t.Errorf("sort = %q, want %q", repo.gotQuery.Sort, tt.wantSort)
			}
			if repo.gotQuery.Limit != tt.wantLim {
				t.Errorf("limit = %d, want %d", repo.gotQuery.Limit, tt.wantLim)
			}
		})
	}
}

// Contradictory input is refused, because the caller can fix it.
func TestSearchRefusesImpossibleInput(t *testing.T) {
	tests := []struct {
		name      string
		in        Query
		wantField string
	}{
		{"min above max", Query{MinPriceMinor: 500, MaxPriceMinor: 100}, "price"},
		{"negative price", Query{MinPriceMinor: -1}, "price"},
		{"absurd query length", Query{Text: strings.Repeat("x", MaxTextLen+1)}, "q"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := newTestService(&fakeRepo{}).Search(context.Background(), tt.in)

			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("error = %v, want a *ValidationError", err)
			}
			if _, ok := verr.Fields[tt.wantField]; !ok {
				t.Errorf("fields = %v, want an entry for %q", verr.Fields, tt.wantField)
			}
		})
	}
}

// Stock is flattened to a boolean: the exact count changes on every sale and
// would rewrite the projection constantly.
func TestProductChangeStoresWhetherItIsBuyable(t *testing.T) {
	for _, tc := range []struct {
		stock int
		want  bool
	}{{0, false}, {1, true}, {50, true}} {
		repo := &fakeRepo{}

		err := newTestService(repo).ApplyProductChange(context.Background(), ProductChange{
			ProductID: "p1", SellerID: "s1", Name: "Mug", Stock: tc.stock,
		})
		if err != nil {
			t.Fatalf("ApplyProductChange() error = %v", err)
		}
		if got := repo.upserted[0].InStock; got != tc.want {
			t.Errorf("stock %d gave InStock=%v, want %v", tc.stock, got, tc.want)
		}
	}
}

func TestDelistingRemovesTheListing(t *testing.T) {
	repo := &fakeRepo{}

	err := newTestService(repo).ApplyProductChange(context.Background(), ProductChange{
		ProductID: "p1", Delisted: true,
	})
	if err != nil {
		t.Fatalf("ApplyProductChange() error = %v", err)
	}

	if len(repo.removed) != 1 || repo.removed[0] != "p1" {
		t.Errorf("removed = %v, want [p1]", repo.removed)
	}
	if len(repo.upserted) != 0 {
		t.Error("a delisted product was also upserted")
	}
}

// An event with no subject can never succeed, so it is dropped rather than
// retried forever behind everything else on the partition.
func TestEventsWithoutAnIDAreDropped(t *testing.T) {
	repo := &fakeRepo{}
	svc := newTestService(repo)
	ctx := context.Background()

	if err := svc.ApplyProductChange(ctx, ProductChange{Name: "Nameless"}); err != nil {
		t.Errorf("ApplyProductChange() error = %v, want nil so the offset commits", err)
	}
	if err := svc.ApplySellerChange(ctx, SellerChange{ShopName: "Nameless"}); err != nil {
		t.Errorf("ApplySellerChange() error = %v, want nil", err)
	}

	if len(repo.upserted) != 0 || repo.sellerCalls != 0 {
		t.Error("a subjectless event was applied")
	}
}

func TestSaleWithNoLinesIsIgnored(t *testing.T) {
	repo := &fakeRepo{}

	if err := newTestService(repo).ApplySale(context.Background(), Sale{OrderID: "o1"}); err != nil {
		t.Fatalf("ApplySale() error = %v", err)
	}
	if len(repo.sales) != 0 {
		t.Error("an empty sale was recorded")
	}
}

func TestSortValidity(t *testing.T) {
	for _, s := range []Sort{SortRelevance, SortNewest, SortPriceAsc, SortPriceDesc, SortBestSelling} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if Sort("cheapest-ish").Valid() {
		t.Error("an unknown sort should not be valid")
	}
}
