package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/product"
)

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *product.ValidationError

	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "validation failed", Fields: verr.Fields})

	case errors.Is(err, errMalformedJSON):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed json body"})

	case errors.Is(err, product.ErrInvalidID):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed product id"})

	case errors.Is(err, product.ErrProductNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "product not found"})

	case errors.Is(err, product.ErrNotOwner):
		writeJSON(w, http.StatusForbidden, errorBody{Error: "this product belongs to another shop"})

	case errors.Is(err, product.ErrUnknownSeller):
		// 409 rather than 404: the account may well have a shop, and this
		// service simply has not seen the event yet. Retrying is reasonable,
		// which is what a conflict says and a not-found does not.
		writeJSON(w, http.StatusConflict, errorBody{
			Error: "no shop is registered for this account yet; if you have just created one, retry shortly",
		})

	default:
		s.log.LogAttrs(r.Context(), slog.LevelError, "unhandled error",
			slog.String("path", r.URL.Path), slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}
