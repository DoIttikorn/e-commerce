// Package handler is the REST driving adapter for the Seller domain.
package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/router"
	"github.com/DoIttikorn/e-commerce/internal/seller"
)

// Service is the slice of the seller service this adapter needs.
type Service interface {
	Register(ctx context.Context, in seller.NewSeller) (seller.Seller, error)
	ByID(ctx context.Context, id string) (seller.Seller, error)
	ByUserID(ctx context.Context, userID string) (seller.Seller, error)
	List(ctx context.Context, limit, offset int) ([]seller.Seller, int, error)
	Update(ctx context.Context, id string, upd seller.Update) (seller.Seller, error)
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
func (s *Server) Register(r router.Router, requireAuth func(http.Handler) http.Handler) {
	r.Group("/api/v1/sellers", func(sellers router.Router) {
		sellers.Use(requireAuth)
		sellers.Handle(http.MethodPost, "/", s.registerSeller)
		sellers.Handle(http.MethodGet, "/", s.listSellers)
		sellers.Handle(http.MethodGet, "/me", s.mySeller)
		sellers.Handle(http.MethodGet, "/{id}", s.getSeller)
		sellers.Handle(http.MethodPatch, "/{id}", s.updateSeller)
	})
}

// requireOwner enforces the same authorization model the User domain uses:
// reads are open to any authenticated caller, writes belong to the owner.
//
// Ownership here is indirect — the shop records which account owns it — so this
// costs a read before the write. That read is the reason the rule lives in one
// helper rather than being repeated per endpoint.
func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request) bool {
	subject, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "you may only modify your own shop"})
		return false
	}

	found, err := s.svc.ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return false
	}
	if found.UserID != subject {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "you may only modify your own shop"})
		return false
	}
	return true
}
