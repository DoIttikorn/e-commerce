// Package handler is the REST driving adapter for the Order domain.
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/order"
	"github.com/DoIttikorn/e-commerce/internal/router"
)

// IdempotencyHeader is required on placement.
//
// Not generated server-side on purpose: a key the server invents is a new key
// on every retry, which is the same as having none. The caller is the only
// party that knows two requests are the same intent.
const IdempotencyHeader = "Idempotency-Key"

// Service is the slice of the order service this adapter needs.
type Service interface {
	Place(ctx context.Context, in order.NewOrder) (order.Placement, error)
	ByID(ctx context.Context, id string) (order.Order, error)
	ListForBuyer(ctx context.Context, buyerUserID string, limit, offset int) ([]order.Order, int, error)
	Cancel(ctx context.Context, id, buyerUserID string) (order.Order, error)
	MarkPaid(ctx context.Context, id, buyerUserID string) (order.Order, error)
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

// Register mounts the routes. Every one requires a token: an order belongs to
// somebody, and there is nothing here worth showing anonymously.
func (s *Server) Register(r router.Router, requireAuth func(http.Handler) http.Handler) {
	r.Group("/api/v1/orders", func(orders router.Router) {
		orders.Use(requireAuth)
		orders.Handle(http.MethodPost, "/", s.placeOrder)
		orders.Handle(http.MethodGet, "/", s.listOrders)
		orders.Handle(http.MethodGet, "/{id}", s.getOrder)
		orders.Handle(http.MethodPost, "/{id}/cancel", s.cancelOrder)
		orders.Handle(http.MethodPost, "/{id}/pay", s.payOrder)
	})
}
