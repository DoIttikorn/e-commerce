package handler

import "net/http"

// login exchanges credentials for a token. Public.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	token, err := s.svc.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		Token:     token.Value,
		ExpiresAt: token.ExpiresAt,
	})
}
