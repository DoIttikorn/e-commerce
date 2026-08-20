package seller

import (
	"context"
	"encoding/json"
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

	// The events the service handed over with the write. In the real adapter
	// these commit in the same transaction as the row, so recording them here
	// is recording what would have been published.
	recorded []sellerv1.SellerEvent
	keys     []string
}

func (f *fakeRepo) NextID() string { return "seller-1" }

// record decodes what the service asked to be written, so a test can assert on
// the event rather than on its bytes.
func (f *fakeRepo) record(events []OutboxEvent) {
	for _, e := range events {
		var decoded sellerv1.SellerEvent
		if err := json.Unmarshal(e.Payload, &decoded); err == nil {
			f.recorded = append(f.recorded, decoded)
			f.keys = append(f.keys, e.Key)
		}
	}
}

func (f *fakeRepo) Create(ctx context.Context, s Seller, events []OutboxEvent) (Seller, error) {
	f.gotSeller = s
	if f.createFn != nil {
		return f.createFn(ctx, s)
	}
	f.record(events)
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

func (f *fakeRepo) Update(ctx context.Context, id string, upd Update, events []OutboxEvent) (Seller, error) {
	f.gotUpdate = upd
	if f.updateFn != nil {
		return f.updateFn(ctx, id, upd)
	}
	f.record(events)
	name := "Shop"
	if upd.ShopName != nil {
		name = *upd.ShopName
	}
	return Seller{ID: id, UserID: "owner", ShopName: name, Status: StatusActive}, nil
}

func newTestService(repo *fakeRepo) Service {
	return NewService(repo, slog.New(slog.DiscardHandler))
}

func TestRegisterOpensAnActiveShopAndAnnouncesIt(t *testing.T) {
	repo := &fakeRepo{}
	svc := newTestService(repo)

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
	if len(repo.recorded) != 1 || repo.recorded[0].Type != sellerv1.EventSellerRegistered {
		t.Fatalf("published = %+v, want one %q event", repo.recorded, sellerv1.EventSellerRegistered)
	}
	// The owner must travel with the event, or a consumer cannot answer
	// "which shop does this account own?" without calling back.
	if repo.recorded[0].UserID != "owner" {
		t.Errorf("event UserID = %q, want the owner", repo.recorded[0].UserID)
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
			svc := newTestService(&fakeRepo{})

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
	repo := &fakeRepo{}
	svc := newTestService(repo)
	newName := "Renamed Shop"

	if _, err := svc.Update(context.Background(), "seller-1", Update{ShopName: &newName}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	if len(repo.recorded) != 1 {
		t.Fatalf("published %d events, want 1", len(repo.recorded))
	}
	if got := repo.recorded[0]; got.Type != sellerv1.EventSellerUpdated || got.ShopName != newName {
		t.Errorf("event = %+v, want a %q carrying the new name", got, sellerv1.EventSellerUpdated)
	}
	// Keyed by seller ID so two renames of the same shop cannot be applied out
	// of order by landing on different partitions.
	if repo.keys[0] != "seller-1" {
		t.Errorf("partition key = %q, want the seller ID", repo.keys[0])
	}
}

// Nothing was written, so nothing may be announced.
func TestUpdateDoesNotAnnounceAFailedWrite(t *testing.T) {
	repo := &fakeRepo{updateFn: func(context.Context, string, Update) (Seller, error) {
		return Seller{}, ErrShopNameTaken
	}}

	name := "Taken"
	_, err := newTestService(repo).Update(context.Background(), "seller-1", Update{ShopName: &name})

	if !errors.Is(err, ErrShopNameTaken) {
		t.Errorf("error = %v, want ErrShopNameTaken", err)
	}
	if len(repo.recorded) != 0 {
		t.Errorf("published %d events after a failed write, want 0", len(repo.recorded))
	}
}

// Nothing here talks to a broker.
//
// This used to be a test that a publish failure did not fail the request, which
// was the best that could be done when the service published directly: the
// write had committed, so the error had nowhere useful to go, and the event was
// lost. With the outbox the question does not arise — the event is handed to
// the repository with the change and committed beside it, so a broker that is
// down delays delivery instead of losing it.
func TestTheEventIsWrittenWithTheChangeRatherThanPublished(t *testing.T) {
	repo := &fakeRepo{}
	name := "Renamed"

	updated, err := newTestService(repo).Update(context.Background(), "seller-1", Update{ShopName: &name})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.ShopName != name {
		t.Errorf("shop name = %q, want %q", updated.ShopName, name)
	}

	if len(repo.recorded) != 1 {
		t.Fatalf("recorded %d events with the write, want 1", len(repo.recorded))
	}
	// The event describes the shop after the change, not before it.
	if repo.recorded[0].ShopName != name {
		t.Errorf("event shop name = %q, want the new %q", repo.recorded[0].ShopName, name)
	}
}

func TestUpdateRejectsAnUnknownStatus(t *testing.T) {
	bogus := Status("liquidated")

	_, err := newTestService(&fakeRepo{}).
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
	_, err := newTestService(&fakeRepo{}).
		Update(context.Background(), "seller-1", Update{})

	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Errorf("error = %v, want a *ValidationError", err)
	}
}
