package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/seller"
)

// writeError maps a domain error to a status code. This mapping is the
// adapter's own; the service returns domain errors and knows no HTTP.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *seller.ValidationError

	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "validation failed", Fields: verr.Fields})

	case errors.Is(err, errMalformedJSON):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed json body"})

	case errors.Is(err, seller.ErrInvalidID):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed seller id"})

	case errors.Is(err, seller.ErrSellerNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "seller not found"})

	case errors.Is(err, seller.ErrShopNameTaken):
		writeJSON(w, http.StatusConflict, errorBody{Error: "shop name already registered"})

	case errors.Is(err, seller.ErrAlreadySeller):
		writeJSON(w, http.StatusConflict, errorBody{Error: "this account already has a shop"})

	default:
		s.log.LogAttrs(r.Context(), slog.LevelError, "unhandled error",
			slog.String("path", r.URL.Path), slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}
