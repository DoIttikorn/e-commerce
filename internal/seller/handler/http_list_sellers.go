package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/seller"
)

// listSellers returns one page of shops.
func (s *Server) listSellers(w http.ResponseWriter, r *http.Request) {
	limit, offset := seller.ClampPage(pagingFrom(r))

	found, total, err := s.svc.List(r.Context(), limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	sellers := make([]sellerResponse, 0, len(found))
	for _, item := range found {
		sellers = append(sellers, toSellerResponse(item))
	}

	writeJSON(w, http.StatusOK, listResponse{
		Sellers: sellers, Total: total, Limit: limit, Offset: offset,
	})
}
