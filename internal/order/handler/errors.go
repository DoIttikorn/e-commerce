package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/order"
)

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *order.ValidationError

	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "validation failed", Fields: verr.Fields})

	case errors.Is(err, errMalformedJSON):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed json body"})

	case errors.Is(err, order.ErrInvalidID):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed order id"})

	case errors.Is(err, order.ErrOrderNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "order not found"})

	case errors.Is(err, order.ErrNotBuyer):
		writeJSON(w, http.StatusForbidden, errorBody{Error: "this order belongs to another buyer"})

	case errors.Is(err, order.ErrOutOfStock):
		// 409, not 400: the request was correct and may well succeed later.
		// A 400 would tell the client to change something, and there is
		// nothing about the request to change.
		writeJSON(w, http.StatusConflict, errorBody{Error: "one or more items are out of stock"})

	case errors.Is(err, order.ErrMixedSellers):
		writeJSON(w, http.StatusBadRequest, errorBody{
			Error: "all items must come from one shop; place one order per shop",
		})

	case errors.Is(err, order.ErrNotPending):
		writeJSON(w, http.StatusConflict, errorBody{Error: "this order is no longer pending"})

	default:
		s.log.LogAttrs(r.Context(), slog.LevelError, "unhandled error",
			slog.String("path", r.URL.Path), slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}
