package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/user"
)

// register creates an account. Public: this is how the first user exists.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
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
