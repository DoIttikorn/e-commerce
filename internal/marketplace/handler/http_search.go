package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/marketplace"
)

type listingResponse struct {
	ProductID  string    `json:"product_id"`
	SellerID   string    `json:"seller_id"`
	SellerName string    `json:"seller_name"`
	Name       string    `json:"name"`
	PriceMinor int64     `json:"price_minor"`
	Currency   string    `json:"currency"`
	InStock    bool      `json:"in_stock"`
	SoldCount  int64     `json:"sold_count"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type searchResponse struct {
	Listings []listingResponse `json:"listings"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
	Sort     string            `json:"sort"`
}

type errorBody struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

// search answers a catalogue query.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := queryFrom(r)

	found, total, err := s.svc.Search(r.Context(), q)
	if err != nil {
		var verr *marketplace.ValidationError
		if errors.As(err, &verr) {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: "validation failed", Fields: verr.Fields})
			return
		}
		s.log.LogAttrs(r.Context(), slog.LevelError, "search failed",
			slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
		return
	}

	listings := make([]listingResponse, 0, len(found))
	for _, l := range found {
		listings = append(listings, listingResponse{
			ProductID:  l.ProductID,
			SellerID:   l.SellerID,
			SellerName: l.SellerName,
			Name:       l.Name,
			PriceMinor: l.PriceMinor,
			Currency:   l.Currency,
			InStock:    l.InStock,
			SoldCount:  l.SoldCount,
			UpdatedAt:  l.UpdatedAt,
		})
	}

	limit, offset := marketplace.ClampPage(q.Limit, q.Offset)
	writeJSON(w, http.StatusOK, searchResponse{
		Listings: listings, Total: total, Limit: limit, Offset: offset, Sort: string(q.Sort),
	})
}

// queryFrom reads the query string leniently.
//
// A search URL is often hand-edited or built by a client that got a parameter
// slightly wrong. Failing the whole request over an unparseable limit would be
// unhelpful; the service clamps what it is given. Genuinely contradictory
// input — a minimum above the maximum — is still refused, because that one the
// caller can fix.
func queryFrom(r *http.Request) marketplace.Query {
	v := r.URL.Query()

	limit, _ := strconv.Atoi(v.Get("limit"))
	offset, _ := strconv.Atoi(v.Get("offset"))
	minPrice, _ := strconv.ParseInt(v.Get("min_price"), 10, 64)
	maxPrice, _ := strconv.ParseInt(v.Get("max_price"), 10, 64)

	return marketplace.Query{
		Text:          v.Get("q"),
		SellerID:      v.Get("seller_id"),
		MinPriceMinor: minPrice,
		MaxPriceMinor: maxPrice,
		InStockOnly:   v.Get("in_stock") == "true",
		Sort:          marketplace.Sort(v.Get("sort")),
		Limit:         limit,
		Offset:        offset,
	}
}
