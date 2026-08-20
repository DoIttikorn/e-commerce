package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/order"
)

// getOrder returns one order, and only to the buyer who placed it.
func (s *Server) getOrder(w http.ResponseWriter, r *http.Request) {
	buyer, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "an order belongs to an account"})
		return
	}

	found, err := s.svc.ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if found.BuyerUserID != buyer {
		s.writeError(w, r, order.ErrNotBuyer)
		return
	}

	writeJSON(w, http.StatusOK, toOrderResponse(found))
}

// listOrders returns the caller's own orders. There is no way to list somebody
// else's: the buyer comes from the token, never from a query parameter.
func (s *Server) listOrders(w http.ResponseWriter, r *http.Request) {
	buyer, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "an order belongs to an account"})
		return
	}

	limit, offset := order.ClampPage(pagingFrom(r))
	found, total, err := s.svc.ListForBuyer(r.Context(), buyer, limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	orders := make([]orderResponse, 0, len(found))
	for _, o := range found {
		orders = append(orders, toOrderResponse(o))
	}
	writeJSON(w, http.StatusOK, listResponse{Orders: orders, Total: total, Limit: limit, Offset: offset})
}
