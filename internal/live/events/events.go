// Package events feeds the Live Commerce domain from other domains' streams.
package events

import (
	"context"
	"encoding/json"
	"log/slog"

	orderv1 "github.com/DoIttikorn/e-commerce/api/order/v1"
	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
	"github.com/DoIttikorn/e-commerce/internal/live"
)

// Applier is the slice of the live service these adapters need.
type Applier interface {
	ApplySellerChange(ctx context.Context, ref live.SellerRef) error
	ApplyPurchase(ctx context.Context, lines []live.PurchasedLine) error
}

type handlerFunc = func(ctx context.Context, key, value []byte) error

// SellerHandler keeps the local copy of who owns which shop.
func SellerHandler(svc Applier, log *slog.Logger) handlerFunc {
	return func(ctx context.Context, key, value []byte) error {
		var event sellerv1.SellerEvent
		if !decode(ctx, log, "seller", key, value, &event) {
			return nil
		}

		return svc.ApplySellerChange(ctx, live.SellerRef{
			SellerID: event.SellerID,
			UserID:   event.UserID,
			ShopName: event.ShopName,
		})
	}
}

// OrderHandler turns a paid order into a message on any stream showing what
// was bought.
//
// The Order domain has never heard of live streams, and nothing about it had to
// change for this to exist. That is the return on publishing an event rather
// than calling the services that care: the third consumer costs the publisher
// exactly as much as the first did.
func OrderHandler(svc Applier, log *slog.Logger) handlerFunc {
	return func(ctx context.Context, key, value []byte) error {
		var event orderv1.OrderEvent
		if !decode(ctx, log, "order", key, value, &event) {
			return nil
		}
		if event.Type != orderv1.EventOrderPaid {
			return nil
		}

		lines := make([]live.PurchasedLine, 0, len(event.Lines))
		for _, l := range event.Lines {
			lines = append(lines, live.PurchasedLine{
				ProductID:   l.ProductID,
				ProductName: l.ProductName,
				Quantity:    l.Quantity,
			})
		}
		return svc.ApplyPurchase(ctx, lines)
	}
}

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
