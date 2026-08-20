package seller

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
)

type fakeRepo struct {
	createFn func(context.Context, Seller) (Seller, error)
	updateFn func(context.Context, string, Update) (Seller, error)

	gotSeller Seller
	gotUpdate Update
}

func (f *fakeRepo) Create(ctx context.Context, s Seller) (Seller, error) {
	f.gotSeller = s
	if f.createFn != nil {
		return f.createFn(ctx, s)
	}
	s.ID = "seller-1"
	return s, nil
}

func (f *fakeRepo) ByID(_ context.Context, id string) (Seller, error) {
	return Seller{ID: id, UserID: "owner", ShopName: "Shop", Status: StatusActive}, nil
}

func (f *fakeRepo) ByUserID(_ context.Context, userID string) (Seller, error) {
	return Seller{ID: "seller-1", UserID: userID}, nil
}

func (f *fakeRepo) List(context.Context, int, int) ([]Seller, int, error) {
	return []Seller{{ID: "seller-1"}}, 1, nil
}

func (f *fakeRepo) Update(ctx context.Context, id string, upd Update) (Seller, error) {
	f.gotUpdate = upd
	if f.updateFn != nil {
		return f.updateFn(ctx, id, upd)
	}
	name := "Shop"
	if upd.ShopName != nil {
		name = *upd.ShopName
	}
	return Seller{ID: id, UserID: "owner", ShopName: name, Status: StatusActive}, nil
}

type fakePublisher struct {
	published []sellerv1.SellerEvent
	keys      []string
	err       error
}

func (f *fakePublisher) Publish(_ context.Context, _, key string, payload any) error {
	if f.err != nil {
		return f.err
	}
	event, ok := payload.(sellerv1.SellerEvent)
	if !ok {
		return errors.New("unexpected payload type")
	}
	f.published = append(f.published, event)
	f.keys = append(f.keys, key)
	return nil
}

func newTestService(repo *fakeRepo, pub *fakePublisher) Service {
	return NewService(repo, pub, slog.New(slog.DiscardHandler))
}

func TestRegisterOpensAnActiveShopAndAnnouncesIt(t *testing.T) {
	repo, pub := &fakeRepo{}, &fakePublisher{}
	svc := newTestService(repo, pub)

	created, err := svc.Register(context.Background(), NewSeller{UserID: "owner", ShopName: "  My Shop  "})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if repo.gotSeller.ShopName != "My Shop" {
		t.Errorf("stored shop name = %q, want it trimmed", repo.gotSeller.ShopName)
	}
	if created.Status != StatusActive {
		t.Errorf("status = %q, want %q", created.Status, StatusActive)
	}
	if len(pub.published) != 1 || pub.published[0].Type != sellerv1.EventSellerRegistered {
		t.Fatalf("published = %+v, want one %q event", pub.published, sellerv1.EventSellerRegistered)
	}
	// The owner must travel with the event, or a consumer cannot answer
	// "which shop does this account own?" without calling back.
	if pub.published[0].UserID != "owner" {
		t.Errorf("event UserID = %q, want the owner", pub.published[0].UserID)
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		in        NewSeller
		wantField string
	}{
		{"no owner", NewSeller{ShopName: "Valid Shop"}, "user_id"},
		{"blank name", NewSeller{UserID: "u", ShopName: "   "}, "shop_name"},
		{"name too short", NewSeller{UserID: "u", ShopName: "ab"}, "shop_name"},
		{"name too long", NewSeller{UserID: "u", ShopName: strings.Repeat("x", MaxShopNameLen+1)}, "shop_name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(&fakeRepo{}, &fakePublisher{})

			_, err := svc.Register(context.Background(), tt.in)

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

// The rename event is the one the Product domain depends on: without it every
// product keeps showing the old shop name forever.
func TestUpdateAnnouncesTheChangeKeyedBySeller(t *testing.T) {
	repo, pub := &fakeRepo{}, &fakePublisher{}
	svc := newTestService(repo, pub)
	newName := "Renamed Shop"

	if _, err := svc.Update(context.Background(), "seller-1", Update{ShopName: &newName}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.published))
	}
	if got := pub.published[0]; got.Type != sellerv1.EventSellerUpdated || got.ShopName != newName {
		t.Errorf("event = %+v, want a %q carrying the new name", got, sellerv1.EventSellerUpdated)
	}
	// Keyed by seller ID so two renames of the same shop cannot be applied out
	// of order by landing on different partitions.
	if pub.keys[0] != "seller-1" {
		t.Errorf("partition key = %q, want the seller ID", pub.keys[0])
	}
}

// Nothing was written, so nothing may be announced.
func TestUpdateDoesNotAnnounceAFailedWrite(t *testing.T) {
	repo := &fakeRepo{updateFn: func(context.Context, string, Update) (Seller, error) {
		return Seller{}, ErrShopNameTaken
	}}
	pub := &fakePublisher{}

	name := "Taken"
	_, err := newTestService(repo, pub).Update(context.Background(), "seller-1", Update{ShopName: &name})

	if !errors.Is(err, ErrShopNameTaken) {
		t.Errorf("error = %v, want ErrShopNameTaken", err)
	}
	if len(pub.published) != 0 {
		t.Errorf("published %d events after a failed write, want 0", len(pub.published))
	}
}

// A publish failure must not fail the request: the write already committed, so
// reporting an error would describe a success as a failure. The event is lost,
// which is the limitation a transactional outbox exists to remove.
func TestUpdateSucceedsWhenPublishingFails(t *testing.T) {
	pub := &fakePublisher{err: errors.New("kafka unreachable")}
	name := "Renamed"

	updated, err := newTestService(&fakeRepo{}, pub).Update(context.Background(), "seller-1", Update{ShopName: &name})

	if err != nil {
		t.Fatalf("Update() error = %v, want the write to be reported as the success it was", err)
	}
	if updated.ShopName != name {
		t.Errorf("shop name = %q, want %q", updated.ShopName, name)
	}
}

func TestUpdateRejectsAnUnknownStatus(t *testing.T) {
	bogus := Status("liquidated")

	_, err := newTestService(&fakeRepo{}, &fakePublisher{}).
		Update(context.Background(), "seller-1", Update{Status: &bogus})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v, want a *ValidationError", err)
	}
	if _, ok := verr.Fields["status"]; !ok {
		t.Errorf("fields = %v, want an entry for status", verr.Fields)
	}
}

func TestUpdateRequiresAtLeastOneField(t *testing.T) {
	_, err := newTestService(&fakeRepo{}, &fakePublisher{}).
		Update(context.Background(), "seller-1", Update{})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Errorf("error = %v, want a *ValidationError", err)
	}
}
