package product

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

type fakeRepo struct {
	createFn func(context.Context, Product) (Product, error)
	byIDFn   func(context.Context, string) (Product, error)
	renameFn func(context.Context, string, string) ([]string, error)

	gotProduct Product
	renames    int
}

func (f *fakeRepo) Create(ctx context.Context, p Product) (Product, error) {
	f.gotProduct = p
	if f.createFn != nil {
		return f.createFn(ctx, p)
	}
	p.ID = "product-1"
	return p, nil
}

func (f *fakeRepo) ByID(ctx context.Context, id string) (Product, error) {
	if f.byIDFn != nil {
		return f.byIDFn(ctx, id)
	}
	return Product{ID: id, SellerID: "seller-1"}, nil
}

func (f *fakeRepo) List(context.Context, string, int, int) ([]Product, int, error) {
	return []Product{{ID: "product-1"}}, 1, nil
}

func (f *fakeRepo) Update(_ context.Context, id string, _ Update) (Product, error) {
	return Product{ID: id}, nil
}

func (f *fakeRepo) Delete(context.Context, string) error { return nil }

func (f *fakeRepo) RenameSeller(ctx context.Context, sellerID, shopName string) ([]string, error) {
	f.renames++
	if f.renameFn != nil {
		return f.renameFn(ctx, sellerID, shopName)
	}
	return []string{"product-1"}, nil
}

type fakeDirectory struct {
	refs    map[string]SellerRef // by seller id
	byUser  map[string]SellerRef
	upserts int
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{refs: map[string]SellerRef{}, byUser: map[string]SellerRef{}}
}

func (f *fakeDirectory) Upsert(_ context.Context, ref SellerRef) error {
	f.upserts++
	f.refs[ref.SellerID] = ref
	f.byUser[ref.UserID] = ref
	return nil
}

func (f *fakeDirectory) Get(_ context.Context, sellerID string) (SellerRef, error) {
	ref, ok := f.refs[sellerID]
	if !ok {
		return SellerRef{}, ErrUnknownSeller
	}
	return ref, nil
}

func (f *fakeDirectory) ByUserID(_ context.Context, userID string) (SellerRef, error) {
	ref, ok := f.byUser[userID]
	if !ok {
		return SellerRef{}, ErrUnknownSeller
	}
	return ref, nil
}

func newTestService(repo *fakeRepo, dir *fakeDirectory) Service {
	return NewService(repo, dir, slog.New(slog.DiscardHandler))
}

func validInput() NewProduct {
	return NewProduct{
		OwnerUserID: "owner", Name: "Blue Mug", PriceMinor: 25000, Currency: "thb", Stock: 10,
	}
}

// The write path resolves the shop from the local directory, so it makes no
// call to the Seller service and does not fail when that service is down.
func TestCreateStampsTheShopNameFromTheLocalDirectory(t *testing.T) {
	repo, dir := &fakeRepo{}, newFakeDirectory()
	_ = dir.Upsert(context.Background(), SellerRef{SellerID: "seller-1", UserID: "owner", ShopName: "My Shop"})

	created, err := newTestService(repo, dir).Create(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if repo.gotProduct.SellerID != "seller-1" || repo.gotProduct.SellerName != "My Shop" {
		t.Errorf("stored seller = %q/%q, want seller-1/My Shop",
			repo.gotProduct.SellerID, repo.gotProduct.SellerName)
	}
	if created.Currency != "THB" {
		t.Errorf("currency = %q, want it upper-cased", created.Currency)
	}
}

// Asynchronous replication has a visible consequence: a shop created a moment
// ago may not be here yet, and the caller is told to retry rather than being
// given a product with a blank shop name.
func TestCreateFailsWhenTheSellerHasNotArrivedYet(t *testing.T) {
	_, err := newTestService(&fakeRepo{}, newFakeDirectory()).Create(context.Background(), validInput())

	if !errors.Is(err, ErrUnknownSeller) {
		t.Errorf("error = %v, want ErrUnknownSeller", err)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*NewProduct)
		wantField string
	}{
		{"no owner", func(p *NewProduct) { p.OwnerUserID = "" }, "owner"},
		{"blank name", func(p *NewProduct) { p.Name = " " }, "name"},
		{"zero price", func(p *NewProduct) { p.PriceMinor = 0 }, "price_minor"},
		{"negative price", func(p *NewProduct) { p.PriceMinor = -1 }, "price_minor"},
		{"bad currency", func(p *NewProduct) { p.Currency = "BAHT" }, "currency"},
		{"negative stock", func(p *NewProduct) { p.Stock = -1 }, "stock"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			tt.mutate(&in)

			_, err := newTestService(&fakeRepo{}, newFakeDirectory()).Create(context.Background(), in)

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

// Folding an event updates both the directory and the copy of the name carried
// on every product the seller owns.
func TestApplySellerEventUpdatesDirectoryAndProducts(t *testing.T) {
	repo, dir := &fakeRepo{}, newFakeDirectory()
	ref := SellerRef{SellerID: "seller-1", UserID: "owner", ShopName: "Renamed", Status: "active"}

	if err := newTestService(repo, dir).ApplySellerEvent(context.Background(), ref); err != nil {
		t.Fatalf("ApplySellerEvent() error = %v", err)
	}

	if got, _ := dir.Get(context.Background(), "seller-1"); got.ShopName != "Renamed" {
		t.Errorf("directory shop name = %q, want %q", got.ShopName, "Renamed")
	}
	if repo.renames != 1 {
		t.Errorf("RenameSeller called %d times, want 1", repo.renames)
	}
}

// At-least-once delivery guarantees a repeat, so the handler must be safe to
// run twice and reach the same state.
func TestApplySellerEventIsIdempotent(t *testing.T) {
	repo, dir := &fakeRepo{}, newFakeDirectory()
	svc := newTestService(repo, dir)
	ref := SellerRef{SellerID: "seller-1", UserID: "owner", ShopName: "Renamed"}

	for range 3 {
		if err := svc.ApplySellerEvent(context.Background(), ref); err != nil {
			t.Fatalf("ApplySellerEvent() error = %v", err)
		}
	}

	if got, _ := dir.Get(context.Background(), "seller-1"); got.ShopName != "Renamed" {
		t.Errorf("directory shop name = %q after three applications, want %q", got.ShopName, "Renamed")
	}
}

// An event with no subject can never succeed, so retrying it forever would
// block the partition for every other event behind it.
func TestApplySellerEventDropsAnEventWithNoID(t *testing.T) {
	repo, dir := &fakeRepo{}, newFakeDirectory()

	err := newTestService(repo, dir).ApplySellerEvent(context.Background(), SellerRef{ShopName: "Nameless"})

	if err != nil {
		t.Errorf("error = %v, want nil so the offset is committed and the partition moves on", err)
	}
	if dir.upserts != 0 || repo.renames != 0 {
		t.Error("a subjectless event was applied")
	}
}

func TestAuthorizeOwner(t *testing.T) {
	dir := newFakeDirectory()
	_ = dir.Upsert(context.Background(), SellerRef{SellerID: "seller-1", UserID: "owner"})
	_ = dir.Upsert(context.Background(), SellerRef{SellerID: "seller-2", UserID: "somebody-else"})
	svc := newTestService(&fakeRepo{}, dir)

	if err := svc.AuthorizeOwner(context.Background(), "owner", "product-1"); err != nil {
		t.Errorf("the owner was refused: %v", err)
	}
	if err := svc.AuthorizeOwner(context.Background(), "somebody-else", "product-1"); !errors.Is(err, ErrNotOwner) {
		t.Errorf("error = %v, want ErrNotOwner for another shop", err)
	}
	// An account with no shop at all owns nothing.
	if err := svc.AuthorizeOwner(context.Background(), "no-shop", "product-1"); !errors.Is(err, ErrNotOwner) {
		t.Errorf("error = %v, want ErrNotOwner for an account with no shop", err)
	}
}
