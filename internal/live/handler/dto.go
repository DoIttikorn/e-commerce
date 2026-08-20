package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/live"
)

const maxBodyBytes = 1 << 20

var errMalformedJSON = errors.New("malformed json body")

type streamResponse struct {
	ID                string     `json:"id"`
	SellerID          string     `json:"seller_id"`
	SellerName        string     `json:"seller_name"`
	Title             string     `json:"title"`
	Status            string     `json:"status"`
	FeaturedProductID string     `json:"featured_product_id,omitempty"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

func toStreamResponse(s live.Stream) streamResponse {
	return streamResponse{
		ID: s.ID, SellerID: s.SellerID, SellerName: s.SellerName,
		Title: s.Title, Status: string(s.Status),
		FeaturedProductID: s.FeaturedProductID,
		StartedAt:         s.StartedAt, EndedAt: s.EndedAt, CreatedAt: s.CreatedAt,
	}
}

type scheduleRequest struct {
	Title string `json:"title"`
}

type featureRequest struct {
	ProductID string `json:"product_id"`
}

type listResponse struct {
	Streams []streamResponse `json:"streams"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
}

type errorBody struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return errMalformedJSON
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pagingFrom(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	return limit, offset
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var verr *live.ValidationError

	switch {
	case errors.As(err, &verr):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "validation failed", Fields: verr.Fields})
	case errors.Is(err, errMalformedJSON):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed json body"})
	case errors.Is(err, live.ErrInvalidID):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "malformed stream id"})
	case errors.Is(err, live.ErrStreamNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "stream not found"})
	case errors.Is(err, live.ErrNotHost):
		writeJSON(w, http.StatusForbidden, errorBody{Error: "this stream belongs to another shop"})
	case errors.Is(err, live.ErrNotLive):
		writeJSON(w, http.StatusConflict, errorBody{Error: "this stream is not live"})
	case errors.Is(err, live.ErrAlreadyLive):
		writeJSON(w, http.StatusConflict, errorBody{Error: "this stream is already live"})
	case errors.Is(err, live.ErrUnknownSeller):
		writeJSON(w, http.StatusConflict, errorBody{
			Error: "no shop is registered for this account yet; if you have just created one, retry shortly",
		})
	default:
		s.log.LogAttrs(r.Context(), slog.LevelError, "unhandled error",
			slog.String("path", r.URL.Path), slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}
