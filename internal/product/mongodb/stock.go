package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/DoIttikorn/e-commerce/internal/product"
)

// ReservationCollectionName records which idempotency keys have been applied.
const ReservationCollectionName = "stock_reservations"

// reservationDoc is keyed by the caller's idempotency key — the order ID — so
// the unique _id index is what makes a retry safe rather than a second charge
// against stock.
type reservationDoc struct {
	ID        string            `bson:"_id"`
	Reserved  []reservedItemDoc `bson:"reserved"`
	CreatedAt time.Time         `bson:"created_at"`
	Released  bool              `bson:"released"`

	// Confirmed is set once the caller has written the order this reservation
	// belongs to. Unconfirmed and old is the signature of a caller that
	// crashed between taking the stock and recording why.
	Confirmed bool `bson:"confirmed"`
}

type reservedItemDoc struct {
	ProductID   string `bson:"product_id"`
	ProductName string `bson:"product_name"`
	SellerID    string `bson:"seller_id"`
	UnitMinor   int64  `bson:"unit_minor"`
	Currency    string `bson:"currency"`
	Quantity    int    `bson:"quantity"`
}

func (d reservedItemDoc) toDomain() product.ReservedItem {
	return product.ReservedItem{
		ProductID:   d.ProductID,
		ProductName: d.ProductName,
		SellerID:    d.SellerID,
		UnitMinor:   d.UnitMinor,
		Currency:    d.Currency,
		Quantity:    d.Quantity,
	}
}

func (r *Repository) reservations() *mongo.Collection {
	return r.coll.Database().Collection(ReservationCollectionName)
}

// Reserve takes stock for every item or none of them.
//
// The whole thing runs in one transaction, which is what the single-node
// replica set is for. Two separate guarantees are needed and neither is enough
// alone:
//
//   - Per item, the decrement is conditional — {stock: {$gte: n}} with $inc —
//     so two buyers racing for the last unit cannot both succeed. That part is
//     atomic on a single document and would need no transaction.
//   - Across items and the idempotency record, the transaction is what makes
//     "all or nothing" true. Without it a failure on the third line would leave
//     the first two decremented.
//
// The cost is write conflicts: two transactions touching the same product
// document abort and retry, which WithTransaction handles but which becomes a
// throughput ceiling on a single very hot product. A flash sale on one item
// wants a different design — pre-allocated buckets, or a counter in Redis —
// and this is the honest limit of this one.
func (r *Repository) Reserve(ctx context.Context, key string, items []product.ReserveItem) ([]product.ReservedItem, error) {
	if len(items) == 0 {
		return nil, nil
	}

	// The idempotency check happens outside the transaction, deliberately.
	//
	// Catching the duplicate inside it and returning success would ask the
	// driver to commit a transaction that already has a failed write in it.
	// That commit fails with a retryable label, WithTransaction retries, the
	// duplicate is still there, and the call spins until its context expires —
	// which is exactly the shape of the bug this comment exists to prevent
	// somebody reintroducing.
	if previous, found, err := r.lookupReservation(ctx, key); err != nil {
		return nil, err
	} else if found {
		return previous, nil
	}

	session, err := r.coll.Database().Client().StartSession()
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}
	defer session.EndSession(ctx)

	out, err := session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		return r.reserveInTx(sc, key, items)
	})
	if err != nil {
		// Two identical requests can race past the check above. The loser sees
		// a duplicate key; by then the winner has committed, so the answer is
		// readable and the retry is satisfied rather than failed.
		if mongo.IsDuplicateKeyError(err) {
			if previous, found, lookupErr := r.lookupReservation(ctx, key); lookupErr == nil && found {
				return previous, nil
			}
		}
		return nil, err
	}

	reserved, _ := out.([]product.ReservedItem)
	return reserved, nil
}

// lookupReservation reads a committed reservation. A transaction makes the
// insert and the decrements one unit, so a record is only ever visible here
// complete.
func (r *Repository) lookupReservation(ctx context.Context, key string) ([]product.ReservedItem, bool, error) {
	var doc reservationDoc
	err := r.reservations().FindOne(ctx, bson.M{"_id": key}).Decode(&doc)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("lookup reservation: %w", err)
	}

	out := make([]product.ReservedItem, 0, len(doc.Reserved))
	for _, d := range doc.Reserved {
		out = append(out, d.toDomain())
	}
	return out, true, nil
}

// reserveInTx claims the key and decrements every line. Any failure returns an
// error so the transaction aborts, which is what makes the whole thing
// all-or-nothing.
func (r *Repository) reserveInTx(ctx context.Context, key string, items []product.ReserveItem) ([]product.ReservedItem, error) {
	// A duplicate here is returned, not handled: Reserve deals with it after
	// the transaction has cleanly aborted.
	if _, err := r.reservations().InsertOne(ctx, reservationDoc{ID: key, CreatedAt: time.Now().UTC()}); err != nil {
		return nil, err
	}

	reserved := make([]product.ReservedItem, 0, len(items))
	docs := make([]reservedItemDoc, 0, len(items))

	for _, item := range items {
		if item.Quantity <= 0 {
			return nil, fmt.Errorf("%w: quantity must be positive", product.ErrInsufficientStock)
		}

		oid, err := bson.ObjectIDFromHex(item.ProductID)
		if err != nil {
			return nil, product.ErrInvalidID
		}

		var doc document
		err = r.coll.FindOneAndUpdate(ctx,
			// The condition is the lock. Nothing is read and then written; the
			// server matches and decrements in one step, so two buyers racing
			// for the last unit cannot both match.
			bson.M{"_id": oid, "stock": bson.M{"$gte": item.Quantity}},
			bson.M{"$inc": bson.M{"stock": -item.Quantity}},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&doc)

		if errors.Is(err, mongo.ErrNoDocuments) {
			// No match means either no such product or not enough stock.
			// Telling them apart costs one read and only on the failure path.
			return nil, r.explainReserveMiss(ctx, oid)
		}
		if err != nil {
			return nil, fmt.Errorf("reserve stock: %w", err)
		}

		line := reservedItemDoc{
			ProductID:   doc.ID.Hex(),
			ProductName: doc.Name,
			SellerID:    doc.SellerID,
			UnitMinor:   doc.PriceMinor,
			Currency:    doc.Currency,
			Quantity:    item.Quantity,
		}
		docs = append(docs, line)
		reserved = append(reserved, line.toDomain())
	}

	if _, err := r.reservations().UpdateByID(ctx, key, bson.M{"$set": bson.M{"reserved": docs}}); err != nil {
		return nil, fmt.Errorf("record reservation: %w", err)
	}
	return reserved, nil
}

func (r *Repository) explainReserveMiss(ctx context.Context, id bson.ObjectID) error {
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Err(); errors.Is(err, mongo.ErrNoDocuments) {
		return product.ErrProductNotFound
	}
	return product.ErrInsufficientStock
}

// Confirm marks a reservation as belonging to a real order, so the reaper
// leaves it alone.
func (r *Repository) Confirm(ctx context.Context, key string) error {
	// No upsert: confirming a key that was never reserved should do nothing
	// rather than invent a reservation record.
	if _, err := r.reservations().UpdateByID(ctx, key,
		bson.M{"$set": bson.M{"confirmed": true}}); err != nil {
		return fmt.Errorf("confirm reservation: %w", err)
	}
	return nil
}

// ReleaseExpired puts back stock held by reservations nobody ever confirmed.
//
// This closes the one gap the saga cannot close on its own: a crash between
// reserving stock and writing the order leaves stock held against nothing, and
// no amount of compensation logic in the caller helps, because the caller is
// the thing that died.
//
// It works one reservation at a time rather than in bulk. Each release is
// already atomic and idempotent, and a bulk update would have to reimplement
// that; the volume here is a handful of abandoned carts, not a batch job.
func (r *Repository) ReleaseExpired(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan)

	cursor, err := r.reservations().Find(ctx, bson.M{
		"confirmed":  false,
		"released":   false,
		"created_at": bson.M{"$lt": cutoff},
	}, options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(500))
	if err != nil {
		return 0, fmt.Errorf("find expired reservations: %w", err)
	}

	var stale []struct {
		ID string `bson:"_id"`
	}
	if err := cursor.All(ctx, &stale); err != nil {
		return 0, fmt.Errorf("decode expired reservations: %w", err)
	}

	released := 0
	for _, item := range stale {
		if err := r.Release(ctx, item.ID, nil); err != nil {
			return released, fmt.Errorf("release expired reservation %s: %w", item.ID, err)
		}
		released++
	}
	return released, nil
}

// Release puts stock back for a key that was reserved.
//
// Releasing an unknown key, or one already released, returns nil. Compensation
// runs on the unhappy path — a cancelled order, a payment that timed out — and
// that is exactly where a caller retries, so refusing a repeat would turn a
// recoverable situation into a stuck one.
func (r *Repository) Release(ctx context.Context, key string, _ []product.ReserveItem) error {
	session, err := r.coll.Database().Client().StartSession()
	if err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		return nil, r.releaseInTx(sc, key)
	})
	return err
}

func (r *Repository) releaseInTx(ctx context.Context, key string) error {
	// Flip released in the same step as reading it, so two concurrent releases
	// cannot both proceed to add the stock back.
	var claimed reservationDoc
	err := r.reservations().FindOneAndUpdate(ctx,
		bson.M{"_id": key, "released": false},
		bson.M{"$set": bson.M{"released": true}},
	).Decode(&claimed)

	if errors.Is(err, mongo.ErrNoDocuments) {
		// Unknown key, or already released. Both are fine.
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim release: %w", err)
	}

	for _, item := range claimed.Reserved {
		oid, err := bson.ObjectIDFromHex(item.ProductID)
		if err != nil {
			continue
		}
		if _, err := r.coll.UpdateByID(ctx, oid, bson.M{"$inc": bson.M{"stock": item.Quantity}}); err != nil {
			return fmt.Errorf("release stock: %w", err)
		}
	}
	return nil
}
