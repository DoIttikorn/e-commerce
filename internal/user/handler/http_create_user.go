package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/user"
)

// createUser creates an account on behalf of an authenticated caller.
//
// It shares its rules with register; the difference is only that this route
// requires a token. The brief lists both, so both exist.
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	created, err := s.svc.Register(r.Context(), user.NewUser{
		Name:     req.Name,
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, toUserResponse(created))
}
