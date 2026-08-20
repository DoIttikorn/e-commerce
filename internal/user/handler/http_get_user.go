package handler

import "net/http"

// getUser returns one user.
//
// The ID is read with r.PathValue, the standard library accessor, not with a
// framework helper — which is what keeps this file free of chi.
func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	found, err := s.svc.ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(found))
}
