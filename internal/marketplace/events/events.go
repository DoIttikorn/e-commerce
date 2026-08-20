// Package events is the driven adapter that feeds the Marketplace projection.
//
// Three streams, three translations, one service. Each handler turns a wire
// format into a domain call and nothing more — the decision about what an
// event means lives in the service, where it can be tested without a broker.
package events

import (
	"context"
	"encoding/json"
	"log/slog"

	orderv1 "github.com/DoIttikorn/e-commerce/api/order/v1"
	productv1 "github.com/DoIttikorn/e-commerce/api/product/v1"
	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
	"github.com/DoIttikorn/e-commerce/internal/marketplace"
)

// Applier is the slice of the marketplace service these adapters need.
type Applier interface {
	ApplyProductChange(ctx context.Context, c marketplace.ProductChange) error
	ApplySellerChange(ctx context.Context, c marketplace.SellerChange) error
	ApplySale(ctx context.Context, s marketplace.Sale) error
}

// handlerFunc is the shape the Kafka consumer expects. It is written out
// rather than named, so it converts freely to the consumer's own type: two
// named function types with identical signatures do not assign to each other,
// and a conversion at every call site is noise.
type handlerFunc = func(ctx context.Context, key, value []byte) error

// ProductHandler folds catalogue events into the projection.
func ProductHandler(svc Applier, log *slog.Logger) handlerFunc {
	return func(ctx context.Context, key, value []byte) error {
		var event productv1.ProductEvent
		if !decode(ctx, log, "product", key, value, &event) {
			return nil
		}

		return svc.ApplyProductChange(ctx, marketplace.ProductChange{
			ProductID:   event.ProductID,
			SellerID:    event.SellerID,
			SellerName:  event.SellerName,
			Name:        event.Name,
			Description: event.Description,
			PriceMinor:  event.PriceMinor,
			Currency:    event.Currency,
			Stock:       event.Stock,
			Delisted:    event.Type == productv1.EventProductDelisted,
		})
	}
}

// SellerHandler folds shop events in.
func SellerHandler(svc Applier, log *slog.Logger) handlerFunc {
	return func(ctx context.Context, key, value []byte) error {
		var event sellerv1.SellerEvent
		if !decode(ctx, log, "seller", key, value, &event) {
			return nil
		}

		return svc.ApplySellerChange(ctx, marketplace.SellerChange{
			SellerID: event.SellerID,
			ShopName: event.ShopName,
			Active:   event.Status == "active",
		})
	}
}

// OrderHandler counts sales, and only from orders that were paid.
//
// Counting placements would rank abandoned baskets alongside real demand.
// Waiting for payment means the ranking reflects what people actually bought,
// which is the only version of "best selling" worth showing.
func OrderHandler(svc Applier, log *slog.Logger) handlerFunc {
	return func(ctx context.Context, key, value []byte) error {
		var event orderv1.OrderEvent
		if !decode(ctx, log, "order", key, value, &event) {
			return nil
		}
		if event.Type != orderv1.EventOrderPaid {
			return nil
		}

		lines := make([]marketplace.SoldLine, 0, len(event.Lines))
		for _, l := range event.Lines {
			lines = append(lines, marketplace.SoldLine{ProductID: l.ProductID, Quantity: l.Quantity})
		}

		return svc.ApplySale(ctx, marketplace.Sale{OrderID: event.OrderID, Lines: lines})
	}
}

// decode reports whether the message was usable.
//
// An undecodable message returns false and the handler returns nil, committing
// the offset. It can never succeed, and retrying it forever would block every
// message behind it on that partition — a single malformed event taking a
// consumer group down is a worse failure than losing the event.
func decode(ctx context.Context, log *slog.Logger, stream string, key, value []byte, into any) bool {
	if err := json.Unmarshal(value, into); err != nil {
		log.LogAttrs(ctx, slog.LevelError, "undecodable event, skipping",
			slog.String("stream", stream),
			slog.String("key", string(key)),
			slog.String("error", err.Error()))
		return false
	}
	return true
}
