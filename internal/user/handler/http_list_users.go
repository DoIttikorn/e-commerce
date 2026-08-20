package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/user"
)

// listUsers returns one page of users.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	// Clamped here as well as in the service, so the response can report the
	// paging that was actually applied rather than what the caller asked for.
	limit, offset := user.ClampPage(pagingFrom(r))

	found, total, err := s.svc.List(r.Context(), limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	users := make([]userResponse, 0, len(found))
	for _, u := range found {
		users = append(users, toUserResponse(u))
	}

	writeJSON(w, http.StatusOK, listResponse{
		Users:  users,
		Total:  total,
		Limit:  limit,
		Offset: offset,
	})
}
