package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/order"
)

const maxBodyBytes = 1 << 20 // 1 MiB

var errMalformedJSON = errors.New("malformed json body")

type lineRequest struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

type placeRequest struct {
	Items []lineRequest `json:"items"`
}

type lineResponse struct {
	ProductID   string `json:"product_id"`
	ProductName string `json:"product_name"`
	UnitMinor   int64  `json:"unit_minor"`
	Quantity    int    `json:"quantity"`
	// SubtotalMinor is computed, not stored, and included so a client does not
	// have to multiply money itself.
	SubtotalMinor int64 `json:"subtotal_minor"`
}

type orderResponse struct {
	ID          string         `json:"id"`
	BuyerUserID string         `json:"buyer_user_id"`
	SellerID    string         `json:"seller_id"`
	Status      string         `json:"status"`
	Lines       []lineResponse `json:"lines"`
	TotalMinor  int64          `json:"total_minor"`
	Currency    string         `json:"currency"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

func toOrderResponse(o order.Order) orderResponse {
	lines := make([]lineResponse, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, lineResponse{
			ProductID:     l.ProductID,
			ProductName:   l.ProductName,
			UnitMinor:     l.UnitMinor,
			Quantity:      l.Quantity,
			SubtotalMinor: l.Subtotal(),
		})
	}
	return orderResponse{
		ID:          o.ID,
		BuyerUserID: o.BuyerUserID,
		SellerID:    o.SellerID,
		Status:      string(o.Status),
		Lines:       lines,
		TotalMinor:  o.TotalMinor,
		Currency:    o.Currency,
		CreatedAt:   o.CreatedAt,
		UpdatedAt:   o.UpdatedAt,
	}
}

type listResponse struct {
	Orders []orderResponse `json:"orders"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
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
