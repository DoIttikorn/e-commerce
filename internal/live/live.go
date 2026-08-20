// Package live is the Live Commerce domain: a seller broadcasting, viewers
// watching, and products being bought while it happens.
//
// It is the only domain here with a realtime surface, and that changes what is
// hard. The others answer a request and forget the caller; this one holds
// thousands of open connections and has to push to them. The consequence runs
// through the whole package: a connection lives on exactly one instance, so
// anything that happens anywhere has to reach every instance before it can
// reach every viewer.
package live

import (
	"errors"
	"time"
)

// Status is where a stream has got to.
type Status string

const (
	StatusScheduled Status = "scheduled"
	StatusLive      Status = "live"
	StatusEnded     Status = "ended"
)

// Valid reports whether s is a status the domain recognises.
func (s Status) Valid() bool {
	return s == StatusScheduled || s == StatusLive || s == StatusEnded
}

// Stream is one broadcast.
type Stream struct {
	ID       string
	SellerID string
	// SellerName is a copy, kept fresh by seller events, for the same reason
	// Product keeps one: a viewer list should not need a call per row.
	SellerName string

	Title  string
	Status Status

	// FeaturedProductID is what the seller is showing right now. Changing it is
	// the most common event in a live stream and the one viewers must see
	// immediately.
	FeaturedProductID string

	StartedAt *time.Time
	EndedAt   *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Event is what a viewer receives.
//
// One envelope with a Type rather than a message per kind, so a client can
// handle what it understands and ignore the rest — the same reasoning as the
// Kafka topics, applied to a socket.
type Event struct {
	Type     string `json:"type"`
	StreamID string `json:"stream_id"`

	// Viewers is present on every event, not just joins. A client that misses
	// one message should not show a stale count until somebody else arrives.
	Viewers int64 `json:"viewers"`

	FeaturedProductID string `json:"featured_product_id,omitempty"`
	ProductName       string `json:"product_name,omitempty"`
	Quantity          int    `json:"quantity,omitempty"`
	Status            string `json:"status,omitempty"`

	At time.Time `json:"at"`
}

// Event types sent to viewers.
const (
	EventViewerJoined    = "viewer.joined"
	EventViewerLeft      = "viewer.left"
	EventProductFeatured = "product.featured"
	EventPurchase        = "purchase"
	EventStreamStarted   = "stream.started"
	EventStreamEnded     = "stream.ended"

	// EventStreamState is sent once, to the viewer who just connected. It is
	// not broadcast: it answers "what am I looking at?" rather than reporting
	// that something changed.
	EventStreamState = "stream.state"
)

var (
	ErrStreamNotFound = errors.New("stream not found")
	ErrInvalidID      = errors.New("malformed stream id")
	ErrNotHost        = errors.New("this stream belongs to another shop")
	ErrNotLive        = errors.New("this stream is not live")
	ErrAlreadyLive    = errors.New("this stream is already live")
)

// ValidationError reports which fields were rejected and why.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "validation failed" }

func newValidationError(fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: fields}
}
