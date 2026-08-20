package handler

import (
	"net/http"

	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/order"
)

// placeOrder reserves stock and records the order.
//
// It returns 201 on a first placement and 200 on a replay of the same
// Idempotency-Key, so a client can tell whether its retry did anything.
func (s *Server) placeOrder(w http.ResponseWriter, r *http.Request) {
	buyer, ok := auth.SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusForbidden, errorBody{Error: "an order belongs to an account"})
		return
	}

	key := r.Header.Get(IdempotencyHeader)
	if key == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Error: "validation failed",
			Fields: map[string]string{
				IdempotencyHeader: "required; send a unique value per order so a retry cannot buy twice",
			},
		})
		return
	}

	var req placeRequest
	if err := decode(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	lines := make([]order.NewLine, 0, len(req.Items))
	for _, i := range req.Items {
		lines = append(lines, order.NewLine{ProductID: i.ProductID, Quantity: i.Quantity})
	}

	placement, err := s.svc.Place(r.Context(), order.NewOrder{
		BuyerUserID:    buyer,
		IdempotencyKey: key,
		Lines:          lines,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// A replayed key returns the order that already existed, so 200 rather
	// than 201. The service says which happened; inferring it from timestamps
	// would be right until two placements landed in the same millisecond.
	status := http.StatusCreated
	if placement.Replayed {
		status = http.StatusOK
	}
	writeJSON(w, status, toOrderResponse(placement.Order))
}
