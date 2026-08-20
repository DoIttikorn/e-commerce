package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/user"
)

// writeError maps a domain error to a status code.
//
// This mapping is the adapter's own: the service returns domain errors and has
// no idea HTTP exists, which is what lets gapi/ map the same errors to gRPC
// codes without either adapter knowing about the other.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *user.ValidationError

	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, errorBody{
			Error:  "validation failed",
			Fields: verr.Fields,
		})

	case errors.Is(err, errMalformedJSON):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed json body"})

	case errors.Is(err, user.ErrInvalidID):
		// Distinct from not-found: the ID could never identify anything.
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed user id"})

	case errors.Is(err, user.ErrInvalidCredentials):
		// One response for an unknown email and a wrong password alike, so the
		// endpoint cannot be used to enumerate registered addresses.
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "invalid credentials"})

	case errors.Is(err, user.ErrUserNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "user not found"})

	case errors.Is(err, user.ErrEmailTaken):
		// Raised by the unique index, not by a read-then-write check.
		writeJSON(w, http.StatusConflict, errorBody{Error: "email already registered"})

	default:
		// Logged in full, reported as nothing: an unexpected error often
		// carries a query, a hostname, or a driver message the client has no
		// business seeing.
		s.log.LogAttrs(r.Context(), slog.LevelError, "unhandled error",
			slog.String("path", r.URL.Path),
			slog.String("error", err.Error()),
		)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}
