// Package handler is the REST driving adapter for the Product domain.
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/product"
	"github.com/DoIttikorn/e-commerce/internal/router"
)

// Service is the slice of the product service this adapter needs.
type Service interface {
	Create(ctx context.Context, in product.NewProduct) (product.Product, error)
	ByID(ctx context.Context, id string) (product.Product, error)
	List(ctx context.Context, sellerID string, limit, offset int) ([]product.Product, int, error)
	Update(ctx context.Context, id string, upd product.Update) (product.Product, error)
	Delete(ctx context.Context, id string) error
	AuthorizeOwner(ctx context.Context, userID, productID string) error
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

// Register mounts the routes.
//
// A catalogue is public and a shop's own edits are not, so authentication is
// applied per route rather than to the whole group. Wrapping the handler at the
// point of registration keeps that visible in the route table, which is where
// somebody looks to answer "is this endpoint protected?".
func (s *Server) Register(r router.Router, requireAuth func(http.Handler) http.Handler) {
	protect := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAuth(h).ServeHTTP
	}

	r.Group("/api/v1/products", func(products router.Router) {
		// Public.
		products.Handle(http.MethodGet, "/", s.listProducts)
		products.Handle(http.MethodGet, "/{id}", s.getProduct)

		// Authenticated, and further restricted to the owning shop.
		products.Handle(http.MethodPost, "/", protect(s.createProduct))
		products.Handle(http.MethodPatch, "/{id}", protect(s.updateProduct))
		products.Handle(http.MethodDelete, "/{id}", protect(s.deleteProduct))
	})
}

// requireOwner allows a write only from the shop that owns the product.
func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request) bool {
	subject, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "this product belongs to another shop"})
		return false
	}

	if err := s.svc.AuthorizeOwner(r.Context(), subject, r.PathValue("id")); err != nil {
		s.writeError(w, r, err)
		return false
	}
	return true
}
