package handler

import "net/http"

// deleteUser removes a user.
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireSelf(w, r) {
		return
	}

	if err := s.svc.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.writeError(w, r, err)
		return
	}

	// 204: nothing useful to say, and a body on a delete is noise.
	w.WriteHeader(http.StatusNoContent)
}
