package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/user"
)

// updateUser changes a name, an email, or both. Omitted fields are left alone.
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireSelf(w, r) {
		return
	}

	var req updateRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	updated, err := s.svc.Update(r.Context(), r.PathValue("id"), user.Update{
		Name:  req.Name,
		Email: req.Email,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, toUserResponse(updated))
}
