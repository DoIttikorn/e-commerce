package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/product"
)

// listProducts returns one page, optionally narrowed with ?seller_id=. Public.
func (s *Server) listProducts(w http.ResponseWriter, r *http.Request) {
	limit, offset := product.ClampPage(pagingFrom(r))
	sellerID := r.URL.Query().Get("seller_id")

	found, total, err := s.svc.List(r.Context(), sellerID, limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	products := make([]productResponse, 0, len(found))
	for _, item := range found {
		products = append(products, toProductResponse(item))
	}

	writeJSON(w, http.StatusOK, listResponse{
		Products: products, Total: total, Limit: limit, Offset: offset,
	})
}
