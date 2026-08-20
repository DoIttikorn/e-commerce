package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/seller"
)

// registerSeller opens a shop for the authenticated account.
func (s *Server) registerSeller(w http.ResponseWriter, r *http.Request) {
	subject, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "a shop belongs to an account"})
		return
	}

	var req registerRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	// The owner comes from the token, never from the body.
	created, err := s.svc.Register(r.Context(), seller.NewSeller{
		UserID:   subject,
		ShopName: req.ShopName,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, toSellerResponse(created))
}
