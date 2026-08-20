// Package events is the driven adapter that feeds the Product domain from the
// Seller domain's event stream.
//
// It is the counterpart of mongodb/: where that adapter turns the domain's
// storage port into MongoDB calls, this one turns an event on a topic into a
// domain call. The service knows neither Kafka nor JSON.
package events

import (
	"context"
	"encoding/json"
	"log/slog"

	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
	"github.com/DoIttikorn/e-commerce/internal/product"
)

// Applier is the slice of the product service this adapter needs.
type Applier interface {
	ApplySellerEvent(ctx context.Context, ref product.SellerRef) error
}

// SellerHandler decodes seller events and folds them into the product service.
//
// The returned function matches the handler the Kafka consumer expects.
// Returning an error leaves the offset uncommitted so the message is retried;
// returning nil for a message that can never succeed is deliberate, because a
// permanent failure retried forever blocks its partition for everyone.
func SellerHandler(svc Applier, log *slog.Logger) func(ctx context.Context, key, value []byte) error {
	return func(ctx context.Context, key, value []byte) error {
		var event sellerv1.SellerEvent
		if err := json.Unmarshal(value, &event); err != nil {
			// Undecodable now means undecodable on every retry. Log it loudly
			// and move on; a real deployment sends this to a dead-letter topic.
			log.LogAttrs(ctx, slog.LevelError, "undecodable seller event, skipping",
				slog.String("key", string(key)),
				slog.String("error", err.Error()))
			return nil
		}

		switch event.Type {
		case sellerv1.EventSellerRegistered, sellerv1.EventSellerUpdated:
		default:
			// An event type this service has no interest in. Not an error:
			// consuming the whole topic and ignoring what does not apply is
			// what lets the producer add event types without coordination.
			return nil
		}

		return svc.ApplySellerEvent(ctx, product.SellerRef{
			SellerID: event.SellerID,
			UserID:   event.UserID,
			ShopName: event.ShopName,
			Status:   event.Status,
		})
	}
}
