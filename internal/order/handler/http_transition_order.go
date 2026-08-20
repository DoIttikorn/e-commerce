package handler

import (
	"context"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/order"
)

// cancelOrder releases the reservation and closes the order.
func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.svc.Cancel)
}

// payOrder stands in for a payment domain that does not exist yet. It turns a
// reservation into a sale; the stock stays taken.
func (s *Server) payOrder(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, s.svc.MarkPaid)
}

// transition is shared because the two endpoints differ only in which service
// method they call. Duplicating the authorization around them is exactly how
// two endpoints end up with two different ideas of who is allowed to act.
func (s *Server) transition(
	w http.ResponseWriter, r *http.Request,
	move func(ctx context.Context, id, buyerUserID string) (order.Order, error),
) {
	buyer, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "an order belongs to an account"})
		return
	}

	moved, err := move(r.Context(), r.PathValue("id"), buyer)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrderResponse(moved))
}
