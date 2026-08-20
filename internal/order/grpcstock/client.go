// Package grpcstock is the driven adapter that lets the Order domain reserve
// stock from the Product service.
//
// It is the only outbound call in the system. Everything else between services
// is an event, because everything else can wait; an order cannot be confirmed
// without knowing the stock was secured.
//
// The Product service's gRPC port is reachable only inside the cluster network
// — compose exposes it without publishing it — and there is no per-user token
// on these calls, because there is no user: one service is talking to another.
// The production answer to that is mutual TLS or a service identity, and it is
// not built here.
package grpcstock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	productv1 "github.com/DoIttikorn/e-commerce/api/product/v1"
	"github.com/DoIttikorn/e-commerce/internal/order"
)

// callTimeout bounds one reservation.
//
// Short on purpose: this call sits on a buyer's checkout, so a Product service
// that has stopped answering must fail the placement quickly rather than hold
// the request open until something upstream gives up first.
const callTimeout = 5 * time.Second

// Client implements order.StockReserver over gRPC.
type Client struct {
	conn   *grpc.ClientConn
	client productv1.StockServiceClient
}

var _ order.StockReserver = (*Client)(nil)

// Dial connects to the Product service.
//
// grpc.NewClient is lazy: it returns before a connection exists and reconnects
// on its own afterwards. That is wanted here — a Product service that is slow
// to start must not stop the Order service from starting at all.
func Dial(target string) (*Client, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial product service at %s: %w", target, err)
	}
	return &Client{conn: conn, client: productv1.NewStockServiceClient(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) Reserve(ctx context.Context, key string, items []order.ReserveLine) ([]order.ReservedLine, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	res, err := c.client.Reserve(ctx, &productv1.ReserveRequest{
		Items:          toProtoItems(items),
		IdempotencyKey: key,
	})
	if err != nil {
		return nil, translate(err)
	}

	out := make([]order.ReservedLine, 0, len(res.GetItems()))
	for _, i := range res.GetItems() {
		out = append(out, order.ReservedLine{
			ProductID:   i.GetProductId(),
			ProductName: i.GetProductName(),
			SellerID:    i.GetSellerId(),
			UnitMinor:   i.GetUnitPriceMinor(),
			Currency:    i.GetCurrency(),
			Quantity:    int(i.GetQuantity()),
		})
	}
	return out, nil
}

func (c *Client) Release(ctx context.Context, key string, items []order.ReserveLine) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	if _, err := c.client.Release(ctx, &productv1.ReleaseRequest{
		Items:          toProtoItems(items),
		IdempotencyKey: key,
	}); err != nil {
		return translate(err)
	}
	return nil
}

func toProtoItems(items []order.ReserveLine) []*productv1.StockItem {
	out := make([]*productv1.StockItem, 0, len(items))
	for _, i := range items {
		out = append(out, &productv1.StockItem{
			ProductId: i.ProductID,
			Quantity:  int32(i.Quantity),
		})
	}
	return out
}

// translate turns another service's gRPC codes into this domain's errors.
//
// The translation happens here, at the adapter, so the Order service never sees
// a gRPC status. That is the same rule as the driving adapters, applied in the
// other direction: a domain should not learn a protocol by being called through
// one, nor by calling out through one.
func translate(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("reserve stock: %w", err)
	}

	switch st.Code() {
	case codes.FailedPrecondition:
		// The request was fine; the world was not. Surfaced unchanged so the
		// buyer is told the truth instead of getting a 500.
		return order.ErrOutOfStock
	case codes.NotFound:
		return &order.ValidationError{Fields: map[string]string{
			"lines": "one or more products do not exist",
		}}
	case codes.InvalidArgument:
		return &order.ValidationError{Fields: map[string]string{
			"lines": st.Message(),
		}}
	case codes.Unavailable, codes.DeadlineExceeded:
		// Worth distinguishing: this is the Product service being unreachable,
		// not the buyer doing anything wrong, and it is retryable.
		return fmt.Errorf("product service unavailable: %w", errors.New(st.Message()))
	default:
		return fmt.Errorf("reserve stock: %s", st.Message())
	}
}
