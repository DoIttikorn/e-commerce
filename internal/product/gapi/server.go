// Package gapi is the gRPC driving adapter for the Product domain.
//
// It carries stock reservation and nothing else. Browsing the catalogue is
// REST, because that faces clients; reserving stock is gRPC, because the only
// caller is another service and the answer cannot wait.
package gapi

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	productv1 "github.com/DoIttikorn/e-commerce/api/product/v1"
	"github.com/DoIttikorn/e-commerce/internal/product"
)

// Service is the slice of the product service this adapter needs.
type Service interface {
	Reserve(ctx context.Context, key string, items []product.ReserveItem) ([]product.ReservedItem, error)
	Release(ctx context.Context, key string, items []product.ReserveItem) error
	Confirm(ctx context.Context, key string) error
}

// Server implements the generated StockServiceServer.
type Server struct {
	productv1.UnimplementedStockServiceServer

	svc Service
}

// New returns a Server backed by svc.
func New(svc Service) *Server {
	return &Server{svc: svc}
}

func (s *Server) Reserve(ctx context.Context, req *productv1.ReserveRequest) (*productv1.ReserveResponse, error) {
	if req.GetIdempotencyKey() == "" {
		// Without a key a retry would take stock twice, so this is refused
		// rather than being quietly best-effort.
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	reserved, err := s.svc.Reserve(ctx, req.GetIdempotencyKey(), toDomainItems(req.GetItems()))
	if err != nil {
		return nil, mapError(err)
	}

	items := make([]*productv1.ReservedItem, 0, len(reserved))
	for _, r := range reserved {
		items = append(items, &productv1.ReservedItem{
			ProductId:      r.ProductID,
			ProductName:    r.ProductName,
			UnitPriceMinor: r.UnitMinor,
			Currency:       r.Currency,
			Quantity:       int32(r.Quantity),
			SellerId:       r.SellerID,
		})
	}
	return &productv1.ReserveResponse{Items: items}, nil
}

func (s *Server) Release(ctx context.Context, req *productv1.ReleaseRequest) (*productv1.ReleaseResponse, error) {
	if req.GetIdempotencyKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	if err := s.svc.Release(ctx, req.GetIdempotencyKey(), toDomainItems(req.GetItems())); err != nil {
		return nil, mapError(err)
	}
	return &productv1.ReleaseResponse{ReleasedItems: int32(len(req.GetItems()))}, nil
}

// Confirm marks a reservation as belonging to an order that now exists.
func (s *Server) Confirm(ctx context.Context, req *productv1.ConfirmRequest) (*productv1.ConfirmResponse, error) {
	if req.GetIdempotencyKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key is required")
	}

	if err := s.svc.Confirm(ctx, req.GetIdempotencyKey()); err != nil {
		return nil, mapError(err)
	}
	return &productv1.ConfirmResponse{}, nil
}

func toDomainItems(items []*productv1.StockItem) []product.ReserveItem {
	out := make([]product.ReserveItem, 0, len(items))
	for _, i := range items {
		out = append(out, product.ReserveItem{
			ProductID: i.GetProductId(),
			Quantity:  int(i.GetQuantity()),
		})
	}
	return out
}

// mapError translates domain errors into gRPC codes.
//
// FailedPrecondition for insufficient stock, not InvalidArgument: the request
// was well formed and the caller did nothing wrong. The state of the world was
// simply not what they needed, and retrying later may well succeed — which is
// exactly the distinction the two codes exist to draw.
func mapError(err error) error {
	switch {
	case errors.Is(err, product.ErrInsufficientStock):
		return status.Error(codes.FailedPrecondition, product.ErrInsufficientStock.Error())
	case errors.Is(err, product.ErrProductNotFound):
		return status.Error(codes.NotFound, "product not found")
	case errors.Is(err, product.ErrInvalidID):
		return status.Error(codes.InvalidArgument, "malformed product id")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
