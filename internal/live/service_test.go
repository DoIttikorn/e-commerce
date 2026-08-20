package live

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

type fakeRepo struct {
	byIDFn      func(context.Context, string) (Stream, error)
	featuringFn func(context.Context, string) ([]Stream, error)

	created   Stream
	updates   []Update
	sellerHit int
}

func (f *fakeRepo) NextID() string { return "stream-1" }

func (f *fakeRepo) Create(_ context.Context, s Stream) (Stream, error) {
	f.created = s
	return s, nil
}

func (f *fakeRepo) ByID(ctx context.Context, id string) (Stream, error) {
	if f.byIDFn != nil {
		return f.byIDFn(ctx, id)
	}
	return Stream{ID: id, SellerID: "seller-1", Status: StatusLive}, nil
}

func (f *fakeRepo) ListLive(context.Context, int, int) ([]Stream, int, error) {
	return []Stream{{ID: "stream-1"}}, 1, nil
}

func (f *fakeRepo) Update(_ context.Context, id string, upd Update) (Stream, error) {
	f.updates = append(f.updates, upd)
	out := Stream{ID: id, SellerID: "seller-1", Status: StatusLive}
	if upd.Status != nil {
		out.Status = *upd.Status
	}
	if upd.FeaturedProductID != nil {
		out.FeaturedProductID = *upd.FeaturedProductID
	}
	return out, nil
}

func (f *fakeRepo) ApplySellerChange(context.Context, string, string) (int64, error) {
	f.sellerHit++
	return 2, nil
}

func (f *fakeRepo) LiveFeaturing(ctx context.Context, productID string) ([]Stream, error) {
	if f.featuringFn != nil {
		return f.featuringFn(ctx, productID)
	}
	return nil, nil
}

type fakeDirectory struct {
	byUser map[string]SellerRef
}

func newFakeDirectory() *fakeDirectory {
	return &fakeDirectory{byUser: map[string]SellerRef{"host": {SellerID: "seller-1", UserID: "host", ShopName: "Shop"}}}
}

func (f *fakeDirectory) Upsert(_ context.Context, ref SellerRef) error {
	f.byUser[ref.UserID] = ref
	return nil
}

func (f *fakeDirectory) ByUserID(_ context.Context, userID string) (SellerRef, error) {
	ref, ok := f.byUser[userID]
	if !ok {
		return SellerRef{}, ErrUnknownSeller
	}
	return ref, nil
}

type fakeBus struct {
	published []Event
	viewers   int64
}

func (f *fakeBus) Join(context.Context, string, string) (int64, error) {
	f.viewers++
	return f.viewers, nil
}

func (f *fakeBus) Leave(context.Context, string, string) (int64, error) {
	f.viewers--
	return f.viewers, nil
}

func (f *fakeBus) Heartbeat(context.Context, string, string) error { return nil }

func (f *fakeBus) Count(context.Context, string) (int64, error) { return f.viewers, nil }

func (f *fakeBus) Publish(_ context.Context, _ string, e Event) error {
	f.published = append(f.published, e)
	return nil
}

func (f *fakeBus) Subscribe(context.Context, string) (<-chan Event, error) {
	ch := make(chan Event, 1)
	return ch, nil
}

func newTestService(repo *fakeRepo, bus *fakeBus) (Service, *fakeDirectory) {
	dir := newFakeDirectory()
	return NewService(repo, dir, bus, bus, slog.New(slog.DiscardHandler)), dir
}

func lastEvent(t *testing.T, bus *fakeBus) Event {
	t.Helper()
	if len(bus.published) == 0 {
		t.Fatal("nothing was broadcast")
	}
	return bus.published[len(bus.published)-1]
}

func TestScheduleTakesTheShopFromTheDirectory(t *testing.T) {
	repo, bus := &fakeRepo{}, &fakeBus{}
	svc, _ := newTestService(repo, bus)

	created, err := svc.Schedule(context.Background(), "host", "  Friday Pottery Sale  ")
	if err != nil {
		t.Fatalf("Schedule() error = %v", err)
	}

	if repo.created.SellerID != "seller-1" || repo.created.SellerName != "Shop" {
		t.Errorf("stream = %+v, want the shop resolved from the local directory", repo.created)
	}
	if created.Title != "Friday Pottery Sale" {
		t.Errorf("title = %q, want it trimmed", created.Title)
	}
	if created.Status != StatusScheduled {
		t.Errorf("status = %q, want %q", created.Status, StatusScheduled)
	}
}

func TestScheduleRefusesAnAccountWithNoShop(t *testing.T) {
	svc, _ := newTestService(&fakeRepo{}, &fakeBus{})

	_, err := svc.Schedule(context.Background(), "not-a-seller", "A Stream")

	if !errors.Is(err, ErrUnknownSeller) {
		t.Errorf("error = %v, want ErrUnknownSeller", err)
	}
}

func TestScheduleValidatesTheTitle(t *testing.T) {
	svc, _ := newTestService(&fakeRepo{}, &fakeBus{})

	for _, title := range []string{"", "  ", "ab"} {
		_, err := svc.Schedule(context.Background(), "host", title)

		var verr *ValidationError
		if !errors.As(err, &verr) {
			t.Errorf("title %q gave %v, want a *ValidationError", title, err)
		}
	}
}

// Every host action tells the audience, because a viewer with no update is
// looking at a screen that has quietly become wrong.
func TestHostActionsBroadcast(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		act    func(Service) error
		want   string
	}{
		{"start", StatusScheduled, func(s Service) error {
			_, err := s.Start(context.Background(), "stream-1", "host")
			return err
		}, EventStreamStarted},
		{"end", StatusLive, func(s Service) error {
			_, err := s.End(context.Background(), "stream-1", "host")
			return err
		}, EventStreamEnded},
		{"feature", StatusLive, func(s Service) error {
			_, err := s.Feature(context.Background(), "stream-1", "host", "product-9")
			return err
		}, EventProductFeatured},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := tt.status
			repo := &fakeRepo{byIDFn: func(_ context.Context, id string) (Stream, error) {
				return Stream{ID: id, SellerID: "seller-1", Status: status}, nil
			}}
			bus := &fakeBus{}
			svc, _ := newTestService(repo, bus)

			if err := tt.act(svc); err != nil {
				t.Fatalf("%s error = %v", tt.name, err)
			}
			if got := lastEvent(t, bus).Type; got != tt.want {
				t.Errorf("broadcast %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOnlyTheHostMayControlAStream(t *testing.T) {
	repo, bus := &fakeRepo{}, &fakeBus{}
	svc, dir := newTestService(repo, bus)
	dir.byUser["other"] = SellerRef{SellerID: "seller-2", UserID: "other", ShopName: "Rival"}

	if _, err := svc.End(context.Background(), "stream-1", "other"); !errors.Is(err, ErrNotHost) {
		t.Errorf("error = %v, want ErrNotHost", err)
	}
	if len(bus.published) != 0 {
		t.Error("a rejected action was still broadcast")
	}
}

func TestFeatureRequiresALiveStream(t *testing.T) {
	repo := &fakeRepo{byIDFn: func(_ context.Context, id string) (Stream, error) {
		return Stream{ID: id, SellerID: "seller-1", Status: StatusScheduled}, nil
	}}
	svc, _ := newTestService(repo, &fakeBus{})

	_, err := svc.Feature(context.Background(), "stream-1", "host", "product-9")

	if !errors.Is(err, ErrNotLive) {
		t.Errorf("error = %v, want ErrNotLive", err)
	}
}

func TestJoiningAStreamThatIsNotLiveIsRefused(t *testing.T) {
	repo := &fakeRepo{byIDFn: func(_ context.Context, id string) (Stream, error) {
		return Stream{ID: id, Status: StatusEnded}, nil
	}}
	svc, _ := newTestService(repo, &fakeBus{})

	if _, _, err := svc.Join(context.Background(), "stream-1", "viewer-1"); !errors.Is(err, ErrNotLive) {
		t.Errorf("error = %v, want ErrNotLive", err)
	}
}

func TestJoinAndLeaveMoveTheViewerCount(t *testing.T) {
	repo, bus := &fakeRepo{}, &fakeBus{}
	svc, _ := newTestService(repo, bus)

	_, count, err := svc.Join(context.Background(), "stream-1", "viewer-1")
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	if count != 1 {
		t.Errorf("count after join = %d, want 1", count)
	}
	if got := lastEvent(t, bus); got.Type != EventViewerJoined || got.Viewers != 1 {
		t.Errorf("broadcast = %+v, want a join carrying the count", got)
	}

	if err := svc.Leave(context.Background(), "stream-1", "viewer-1"); err != nil {
		t.Fatalf("Leave() error = %v", err)
	}
	if got := lastEvent(t, bus); got.Type != EventViewerLeft || got.Viewers != 0 {
		t.Errorf("broadcast = %+v, want a leave carrying the count", got)
	}
}

// A paid order reaches whichever streams are showing what was bought — and the
// Order domain never had to know this service exists.
func TestAPurchaseReachesTheStreamsShowingThatProduct(t *testing.T) {
	repo := &fakeRepo{featuringFn: func(_ context.Context, productID string) ([]Stream, error) {
		if productID == "featured" {
			return []Stream{{ID: "stream-1"}, {ID: "stream-2"}}, nil
		}
		return nil, nil
	}}
	bus := &fakeBus{}
	svc, _ := newTestService(repo, bus)

	err := svc.ApplyPurchase(context.Background(), []PurchasedLine{
		{ProductID: "featured", ProductName: "Blue Mug", Quantity: 2},
		{ProductID: "not-on-air", ProductName: "Spoon", Quantity: 1},
	})
	if err != nil {
		t.Fatalf("ApplyPurchase() error = %v", err)
	}

	if len(bus.published) != 2 {
		t.Fatalf("broadcast %d messages, want 2 — one per stream showing it", len(bus.published))
	}
	for _, e := range bus.published {
		if e.Type != EventPurchase || e.ProductName != "Blue Mug" || e.Quantity != 2 {
			t.Errorf("event = %+v, want a purchase of two Blue Mugs", e)
		}
	}
}

func TestSellerEventsKeepTheCopiedShopNameHonest(t *testing.T) {
	repo, bus := &fakeRepo{}, &fakeBus{}
	svc, dir := newTestService(repo, bus)

	err := svc.ApplySellerChange(context.Background(), SellerRef{
		SellerID: "seller-1", UserID: "host", ShopName: "Renamed Shop",
	})
	if err != nil {
		t.Fatalf("ApplySellerChange() error = %v", err)
	}

	if repo.sellerHit != 1 {
		t.Errorf("the repository was updated %d times, want 1", repo.sellerHit)
	}
	if dir.byUser["host"].ShopName != "Renamed Shop" {
		t.Error("the directory was not updated")
	}
}

func TestStatusValidity(t *testing.T) {
	for _, s := range []Status{StatusScheduled, StatusLive, StatusEnded} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if Status("buffering").Valid() {
		t.Error("an unknown status should not be valid")
	}
	_ = time.Now
}
