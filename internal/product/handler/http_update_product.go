package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/product"
)

// updateProduct changes the fields the caller supplied.
func (s *Server) updateProduct(w http.ResponseWriter, r *http.Request) {
	if !s.requireOwner(w, r) {
		return
	}

	var req updateRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	updated, err := s.svc.Update(r.Context(), r.PathValue("id"), product.Update{
		Name:        req.Name,
		Description: req.Description,
		PriceMinor:  req.PriceMinor,
		Stock:       req.Stock,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toProductResponse(updated))
}
