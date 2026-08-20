// Package handler is the driving adapter for the Live Commerce domain: REST
// for the host, and a WebSocket for viewers.
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/live"
	"github.com/DoIttikorn/e-commerce/internal/router"
)

// Service is the slice of the live service this adapter needs.
type Service interface {
	Schedule(ctx context.Context, hostUserID, title string) (live.Stream, error)
	Start(ctx context.Context, id, hostUserID string) (live.Stream, error)
	End(ctx context.Context, id, hostUserID string) (live.Stream, error)
	Feature(ctx context.Context, id, hostUserID, productID string) (live.Stream, error)
	ByID(ctx context.Context, id string) (live.Stream, error)
	ListLive(ctx context.Context, limit, offset int) ([]live.Stream, int, error)
	Join(ctx context.Context, streamID, viewerID string) (<-chan live.Event, int64, error)
	Leave(ctx context.Context, streamID, viewerID string) error
}

// Presence is the part of presence the socket needs directly, to keep a
// connected viewer counted for as long as they are connected.
type Presence interface {
	Heartbeat(ctx context.Context, streamID, viewerID string) error
}

// Server holds the dependencies the endpoints share.
type Server struct {
	svc      Service
	presence Presence
	log      *slog.Logger
}

// New returns a Server.
func New(svc Service, presence Presence, log *slog.Logger) *Server {
	return &Server{svc: svc, presence: presence, log: log}
}

// Register mounts the routes.
//
// Watching is public. That is a product decision — a shop window nobody may
// look through is not much of a shop — but it also removes a security problem:
// a browser cannot set headers on a WebSocket handshake, so an authenticated
// socket ends up with the token in the query string, which is the one place
// credentials are guaranteed to be written to somebody's access log.
func (s *Server) Register(r router.Router, requireAuth func(http.Handler) http.Handler) {
	protect := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAuth(h).ServeHTTP
	}

	r.Group("/api/v1/live/streams", func(streams router.Router) {
		// Public: browse and watch.
		streams.Handle(http.MethodGet, "/", s.listStreams)
		streams.Handle(http.MethodGet, "/{id}", s.getStream)
		streams.Handle(http.MethodGet, "/{id}/watch", s.watch)

		// The host's own controls.
		streams.Handle(http.MethodPost, "/", protect(s.scheduleStream))
		streams.Handle(http.MethodPost, "/{id}/start", protect(s.startStream))
		streams.Handle(http.MethodPost, "/{id}/end", protect(s.endStream))
		streams.Handle(http.MethodPost, "/{id}/feature", protect(s.featureProduct))
	})
}
