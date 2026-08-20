package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/product"
)

const maxBodyBytes = 1 << 20 // 1 MiB

var errMalformedJSON = errors.New("malformed json body")

// productResponse names the price field price_minor so nobody reads it as baht.
// Money crosses the wire as an integer count of minor units plus a currency,
// never as a decimal that a JSON parser is free to turn into a float.
type productResponse struct {
	ID          string    `json:"id"`
	SellerID    string    `json:"seller_id"`
	SellerName  string    `json:"seller_name"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	PriceMinor  int64     `json:"price_minor"`
	Currency    string    `json:"currency"`
	Stock       int       `json:"stock"`
	CreatedAt   time.Time `json:"created_at"`
}

func toProductResponse(p product.Product) productResponse {
	return productResponse{
		ID:          p.ID,
		SellerID:    p.SellerID,
		SellerName:  p.SellerName,
		Name:        p.Name,
		Description: p.Description,
		PriceMinor:  p.PriceMinor,
		Currency:    p.Currency,
		Stock:       p.Stock,
		CreatedAt:   p.CreatedAt,
	}
}

// createRequest carries no seller_id: the shop is resolved from the token.
type createRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Currency    string `json:"currency"`
	Stock       int    `json:"stock"`
}

type updateRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	PriceMinor  *int64  `json:"price_minor"`
	Stock       *int    `json:"stock"`
}

type listResponse struct {
	Products []productResponse `json:"products"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
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
