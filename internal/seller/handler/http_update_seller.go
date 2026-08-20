package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/seller"
)

// updateSeller changes a shop name, a status, or both.
//
// A successful change publishes an event, which is what keeps the copy of the
// shop name that Product holds from going stale.
func (s *Server) updateSeller(w http.ResponseWriter, r *http.Request) {
	if !s.requireOwner(w, r) {
		return
	}

	var req updateRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	upd := seller.Update{ShopName: req.ShopName}
	if req.Status != nil {
		status := seller.Status(*req.Status)
		upd.Status = &status
	}

	updated, err := s.svc.Update(r.Context(), r.PathValue("id"), upd)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toSellerResponse(updated))
}
