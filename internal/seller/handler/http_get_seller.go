package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
)

// getSeller returns one shop.
func (s *Server) getSeller(w http.ResponseWriter, r *http.Request) {
	found, err := s.svc.ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSellerResponse(found))
}

// mySeller returns the caller's own shop, so a client does not have to remember
// its shop ID to manage it.
func (s *Server) mySeller(w http.ResponseWriter, r *http.Request) {
	subject, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "a shop belongs to an account"})
		return
	}

	found, err := s.svc.ByUserID(r.Context(), subject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSellerResponse(found))
}
