package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/seller"
)

const maxBodyBytes = 1 << 20 // 1 MiB

var errMalformedJSON = errors.New("malformed json body")

type sellerResponse struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ShopName  string    `json:"shop_name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

func toSellerResponse(s seller.Seller) sellerResponse {
	return sellerResponse{
		ID:        s.ID,
		UserID:    s.UserID,
		ShopName:  s.ShopName,
		Status:    string(s.Status),
		CreatedAt: s.CreatedAt,
	}
}

// registerRequest carries no user_id: the owner is taken from the token, so a
// caller cannot open a shop in somebody else's name.
type registerRequest struct {
	ShopName string `json:"shop_name"`
}

type updateRequest struct {
	ShopName *string `json:"shop_name"`
	Status   *string `json:"status"`
}

type listResponse struct {
	Sellers []sellerResponse `json:"sellers"`
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
