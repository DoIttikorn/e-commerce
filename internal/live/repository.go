package live

import "context"

// Repository is the persistence port for streams.
type Repository interface {
	NextID() string
	Create(ctx context.Context, s Stream) (Stream, error)
	ByID(ctx context.Context, id string) (Stream, error)
	ListLive(ctx context.Context, limit, offset int) (streams []Stream, total int, err error)
	Update(ctx context.Context, id string, upd Update) (Stream, error)

	// ApplySellerChange keeps the copied shop name honest, as everywhere else.
	ApplySellerChange(ctx context.Context, sellerID, shopName string) (int64, error)

	// LiveFeaturing returns the live streams currently showing a product.
	//
	// This is how a purchase finds its way to a broadcast: an order event says
	// what was bought, and this says who is showing it right now.
	LiveFeaturing(ctx context.Context, productID string) ([]Stream, error)
}

// SellerRef is Live's own record of a shop, built from seller events. Not
// seller.Seller: sharing that type would couple two services that are deployed
// and scaled apart.
type SellerRef struct {
	SellerID string
	UserID   string
	ShopName string
}

// SellerDirectory is Live's local copy of who owns which shop, so hosting a
// stream needs no call to the Seller service.
type SellerDirectory interface {
	Upsert(ctx context.Context, ref SellerRef) error
	ByUserID(ctx context.Context, userID string) (SellerRef, error)
}

// Update carries only the fields the caller supplied.
type Update struct {
	Title             *string
	Status            *Status
	FeaturedProductID *string
}

// IsEmpty reports whether the update would change nothing.
func (u Update) IsEmpty() bool {
	return u.Title == nil && u.Status == nil && u.FeaturedProductID == nil
}

// Presence tracks how many viewers a stream has, across every instance.
//
// It is a port rather than a map in memory, and that is the entire point. With
// two instances of this service, a map would give each one its own idea of the
// audience and both would be wrong. The count has to live somewhere both can
// see, which in practice means Redis.
type Presence interface {
	// Join records a viewer and returns the new total.
	Join(ctx context.Context, streamID, viewerID string) (int64, error)

	// Leave removes one and returns the new total.
	Leave(ctx context.Context, streamID, viewerID string) (int64, error)

	// Heartbeat refreshes a viewer's presence.
	//
	// Presence has to expire on its own. A viewer whose laptop lid closes sends
	// no goodbye, and an instance that crashes sends none for anybody it was
	// holding — without a heartbeat those viewers stay in the count forever,
	// and the number slowly becomes fiction.
	Heartbeat(ctx context.Context, streamID, viewerID string) error

	// Count returns the current total.
	Count(ctx context.Context, streamID string) (int64, error)
}

// Broadcaster fans an event out to every viewer of a stream, wherever they are
// connected.
//
// A WebSocket lives on one instance. A purchase consumed by instance B has to
// reach a viewer holding a socket on instance A, and no amount of local
// bookkeeping can do that — the message has to leave the process. Redis pub/sub
// is what carries it; this interface is what keeps the domain from knowing so.
type Broadcaster interface {
	// Publish sends an event to every subscriber of a stream.
	Publish(ctx context.Context, streamID string, e Event) error

	// Subscribe delivers events for a stream until ctx is cancelled. The
	// returned channel is closed when it stops.
	Subscribe(ctx context.Context, streamID string) (<-chan Event, error)
}
