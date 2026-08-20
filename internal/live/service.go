package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

const (
	MinTitleLen = 3
	MaxTitleLen = 140

	DefaultPageSize = 20
	MaxPageSize     = 60
)

// ErrUnknownSeller means no seller event has arrived for this account yet.
var ErrUnknownSeller = errors.New("no shop is registered for this account")

// Service is everything the Live Commerce domain can do.
type Service interface {
	// Schedule creates a stream for the caller's own shop.
	Schedule(ctx context.Context, hostUserID, title string) (Stream, error)

	// Start puts a scheduled stream on air.
	Start(ctx context.Context, id, hostUserID string) (Stream, error)

	// End takes it off air. Viewers are told; their sockets are not closed
	// from here, because a client that knows the stream ended can say so
	// better than a dropped connection can.
	End(ctx context.Context, id, hostUserID string) (Stream, error)

	// Feature changes what the stream is showing, and tells every viewer.
	Feature(ctx context.Context, id, hostUserID, productID string) (Stream, error)

	ByID(ctx context.Context, id string) (Stream, error)
	ListLive(ctx context.Context, limit, offset int) (streams []Stream, total int, err error)

	// Join subscribes a viewer. The channel carries events until ctx is
	// cancelled; the count is what the audience is at the moment of joining.
	Join(ctx context.Context, streamID, viewerID string) (<-chan Event, int64, error)

	// Leave removes a viewer and tells the rest.
	Leave(ctx context.Context, streamID, viewerID string) error

	// ApplySellerChange folds in a shop event.
	ApplySellerChange(ctx context.Context, ref SellerRef) error

	// ApplyPurchase turns a completed order into a message on every stream
	// currently showing what was bought.
	ApplyPurchase(ctx context.Context, lines []PurchasedLine) error
}

// PurchasedLine is one line of a paid order, as this domain cares about it.
type PurchasedLine struct {
	ProductID   string
	ProductName string
	Quantity    int
}

type service struct {
	repo      Repository
	directory SellerDirectory
	presence  Presence
	bus       Broadcaster
	log       *slog.Logger
}

var _ Service = (*service)(nil)

// NewService wires the domain to its adapters.
func NewService(
	repo Repository, directory SellerDirectory,
	presence Presence, bus Broadcaster, log *slog.Logger,
) Service {
	return &service{repo: repo, directory: directory, presence: presence, bus: bus, log: log}
}

func (s *service) Schedule(ctx context.Context, hostUserID, title string) (Stream, error) {
	title = strings.TrimSpace(title)
	fields := map[string]string{}
	if len(title) < MinTitleLen || len(title) > MaxTitleLen {
		fields["title"] = fmt.Sprintf("must be between %d and %d characters", MinTitleLen, MaxTitleLen)
	}
	if err := newValidationError(fields); err != nil {
		return Stream{}, err
	}

	ref, err := s.directory.ByUserID(ctx, hostUserID)
	if err != nil {
		return Stream{}, err
	}

	now := time.Now().UTC()
	return s.repo.Create(ctx, Stream{
		ID:         s.repo.NextID(),
		SellerID:   ref.SellerID,
		SellerName: ref.ShopName,
		Title:      title,
		Status:     StatusScheduled,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

func (s *service) Start(ctx context.Context, id, hostUserID string) (Stream, error) {
	found, err := s.authorize(ctx, id, hostUserID)
	if err != nil {
		return Stream{}, err
	}
	if found.Status == StatusLive {
		return Stream{}, ErrAlreadyLive
	}
	if found.Status == StatusEnded {
		return Stream{}, ErrNotLive
	}

	status := StatusLive
	started, err := s.repo.Update(ctx, id, Update{Status: &status})
	if err != nil {
		return Stream{}, err
	}

	s.broadcast(ctx, id, Event{Type: EventStreamStarted, Status: string(StatusLive)})
	return started, nil
}

func (s *service) End(ctx context.Context, id, hostUserID string) (Stream, error) {
	found, err := s.authorize(ctx, id, hostUserID)
	if err != nil {
		return Stream{}, err
	}
	if found.Status != StatusLive {
		return Stream{}, ErrNotLive
	}

	status := StatusEnded
	ended, err := s.repo.Update(ctx, id, Update{Status: &status})
	if err != nil {
		return Stream{}, err
	}

	s.broadcast(ctx, id, Event{Type: EventStreamEnded, Status: string(StatusEnded)})
	return ended, nil
}

func (s *service) Feature(ctx context.Context, id, hostUserID, productID string) (Stream, error) {
	if strings.TrimSpace(productID) == "" {
		return Stream{}, &ValidationError{Fields: map[string]string{"product_id": "is required"}}
	}

	found, err := s.authorize(ctx, id, hostUserID)
	if err != nil {
		return Stream{}, err
	}
	if found.Status != StatusLive {
		return Stream{}, ErrNotLive
	}

	updated, err := s.repo.Update(ctx, id, Update{FeaturedProductID: &productID})
	if err != nil {
		return Stream{}, err
	}

	s.broadcast(ctx, id, Event{Type: EventProductFeatured, FeaturedProductID: productID})
	return updated, nil
}

func (s *service) ByID(ctx context.Context, id string) (Stream, error) {
	return s.repo.ByID(ctx, id)
}

func (s *service) ListLive(ctx context.Context, limit, offset int) ([]Stream, int, error) {
	limit, offset = ClampPage(limit, offset)
	return s.repo.ListLive(ctx, limit, offset)
}

func (s *service) Join(ctx context.Context, streamID, viewerID string) (<-chan Event, int64, error) {
	found, err := s.repo.ByID(ctx, streamID)
	if err != nil {
		return nil, 0, err
	}
	if found.Status != StatusLive {
		return nil, 0, ErrNotLive
	}

	// Subscribe before counting, so a joiner cannot miss the very event that
	// announces their own arrival.
	events, err := s.bus.Subscribe(ctx, streamID)
	if err != nil {
		return nil, 0, fmt.Errorf("subscribe: %w", err)
	}

	count, err := s.presence.Join(ctx, streamID, viewerID)
	if err != nil {
		return nil, 0, fmt.Errorf("join presence: %w", err)
	}

	s.broadcast(ctx, streamID, Event{Type: EventViewerJoined, Viewers: count})
	return events, count, nil
}

func (s *service) Leave(ctx context.Context, streamID, viewerID string) error {
	count, err := s.presence.Leave(ctx, streamID, viewerID)
	if err != nil {
		return fmt.Errorf("leave presence: %w", err)
	}

	s.broadcast(ctx, streamID, Event{Type: EventViewerLeft, Viewers: count})
	return nil
}

func (s *service) ApplySellerChange(ctx context.Context, ref SellerRef) error {
	if ref.SellerID == "" {
		s.log.LogAttrs(ctx, slog.LevelError, "seller event without an id, dropping")
		return nil
	}
	if err := s.directory.Upsert(ctx, ref); err != nil {
		return err
	}
	_, err := s.repo.ApplySellerChange(ctx, ref.SellerID, ref.ShopName)
	return err
}

// ApplyPurchase is the link between a completed order and a live audience.
//
// The Order domain has never heard of streams and does not need to: it says
// what was bought, and this asks its own database who is showing it. Adding a
// second consumer of that same event later costs the Order service nothing.
func (s *service) ApplyPurchase(ctx context.Context, lines []PurchasedLine) error {
	for _, line := range lines {
		streams, err := s.repo.LiveFeaturing(ctx, line.ProductID)
		if err != nil {
			return err
		}

		for _, stream := range streams {
			count, err := s.presence.Count(ctx, stream.ID)
			if err != nil {
				// Not fatal: a purchase message without an audience figure is
				// better than no purchase message.
				count = 0
			}
			s.broadcast(ctx, stream.ID, Event{
				Type:              EventPurchase,
				FeaturedProductID: line.ProductID,
				ProductName:       line.ProductName,
				Quantity:          line.Quantity,
				Viewers:           count,
			})
		}
	}
	return nil
}

// broadcast fills in what every event carries and sends it.
//
// A failure is logged, not returned. The thing that mattered — the stream
// changing, the order completing — has already happened and been recorded; a
// viewer missing one frame of a live feed is not worth failing that for.
func (s *service) broadcast(ctx context.Context, streamID string, e Event) {
	e.StreamID = streamID
	e.At = time.Now().UTC()

	if e.Viewers == 0 {
		if count, err := s.presence.Count(ctx, streamID); err == nil {
			e.Viewers = count
		}
	}

	if err := s.bus.Publish(ctx, streamID, e); err != nil {
		s.log.LogAttrs(ctx, slog.LevelError, "broadcast failed",
			slog.String("stream_id", streamID),
			slog.String("type", e.Type),
			slog.String("error", err.Error()))
	}
}

func (s *service) authorize(ctx context.Context, id, hostUserID string) (Stream, error) {
	found, err := s.repo.ByID(ctx, id)
	if err != nil {
		return Stream{}, err
	}

	ref, err := s.directory.ByUserID(ctx, hostUserID)
	if err != nil {
		if errors.Is(err, ErrUnknownSeller) {
			return Stream{}, ErrNotHost
		}
		return Stream{}, err
	}
	if ref.SellerID != found.SellerID {
		return Stream{}, ErrNotHost
	}
	return found, nil
}

// ClampPage applies the paging bounds.
func ClampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}
