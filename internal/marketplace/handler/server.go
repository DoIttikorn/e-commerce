// Package handler is the REST driving adapter for the Marketplace domain.
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/marketplace"
	"github.com/DoIttikorn/e-commerce/internal/router"
)

// Service is the slice of the marketplace service this adapter needs.
//
// One method. The rest of the service is written by events, and nothing over
// HTTP is allowed to write a read model — a projection you can edit is no
// longer a projection of anything.
type Service interface {
	Search(ctx context.Context, q marketplace.Query) ([]marketplace.Listing, int, error)
}

// Server holds the dependencies the endpoints share.
type Server struct {
	svc Service
	log *slog.Logger
}

// New returns a Server.
func New(svc Service, log *slog.Logger) *Server {
	return &Server{svc: svc, log: log}
}

// Register mounts the routes. Browsing a marketplace is public — requiring a
// token to look at a shop window would be an odd way to run one.
func (s *Server) Register(r router.Router, _ func(http.Handler) http.Handler) {
	r.Group("/api/v1/marketplace", func(m router.Router) {
		m.Handle(http.MethodGet, "/listings", s.search)
	})
}
