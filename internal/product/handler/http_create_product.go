package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/product"
)

// createProduct lists an item in the caller's own shop.
func (s *Server) createProduct(w http.ResponseWriter, r *http.Request) {
	subject, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "a product belongs to a shop"})
		return
	}

	var req createRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	created, err := s.svc.Create(r.Context(), product.NewProduct{
		OwnerUserID: subject,
		Name:        req.Name,
		Description: req.Description,
		PriceMinor:  req.PriceMinor,
		Currency:    req.Currency,
		Stock:       req.Stock,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, toProductResponse(created))
}
