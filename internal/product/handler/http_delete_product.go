package handler

import "net/http"

// deleteProduct removes a product from its shop.
func (s *Server) deleteProduct(w http.ResponseWriter, r *http.Request) {
	if !s.requireOwner(w, r) {
		return
	}

	if err := s.svc.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
