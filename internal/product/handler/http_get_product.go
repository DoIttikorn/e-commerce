package handler

import "net/http"

// getProduct returns one product. Public: a catalogue is meant to be read.
//
// This is the read the Redis decorator serves, so it is also the endpoint whose
// hit rate is worth watching in product_cache_lookups_total.
func (s *Server) getProduct(w http.ResponseWriter, r *http.Request) {
	found, err := s.svc.ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toProductResponse(found))
}
