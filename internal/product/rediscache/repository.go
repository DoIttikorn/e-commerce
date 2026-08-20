// Package rediscache decorates a product.Repository with a Redis read-through
// cache.
//
// It is a decorator rather than a branch inside the service, which is the
// payoff of the Repository port: the service is written as if the cache does
// not exist, and turning caching off is one line in main.go. Nothing in the
// domain imports Redis.
//
// It is named rediscache rather than redis so it can import the client it wraps.
package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/DoIttikorn/e-commerce/internal/product"
)

// keyPrefix is versioned so a change to the cached shape cannot read documents
// written in the old one. Bumping v1 to v2 retires every stale entry at once,
// without a flush.
const keyPrefix = "product:v1:"

// Repository implements product.Repository over another implementation.
type Repository struct {
	inner product.Repository
	rdb   *redis.Client
	ttl   time.Duration
	log   *slog.Logger

	lookups *prometheus.CounterVec
}

var _ product.Repository = (*Repository)(nil)

// NextID is passed straight through: minting an identifier is the store's job,
// and a cache has no opinion about it.
func (r *Repository) NextID() string { return r.inner.NextID() }

// New returns a caching Repository. Registering the counters here makes the
// cache measurable: a cache whose hit rate nobody can see is a guess.
func New(
	inner product.Repository,
	rdb *redis.Client,
	ttl time.Duration,
	log *slog.Logger,
	reg prometheus.Registerer,
) *Repository {
	lookups := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "product_cache_lookups_total",
		Help: "Product cache lookups by outcome.",
	}, []string{"result"})
	reg.MustRegister(lookups)

	return &Repository{inner: inner, rdb: rdb, ttl: ttl, log: log, lookups: lookups}
}

func key(id string) string { return keyPrefix + id }

// ByID serves from the cache when it can, and from the database when it cannot.
//
// Every Redis failure falls through to the inner repository rather than being
// returned. A cache is an optimisation: if it makes the service fail when it is
// unavailable, it has turned into a dependency and made availability worse
// rather than better.
func (r *Repository) ByID(ctx context.Context, id string) (product.Product, error) {
	if cached, ok := r.fromCache(ctx, id); ok {
		r.lookups.WithLabelValues("hit").Inc()
		return cached, nil
	}

	found, err := r.inner.ByID(ctx, id)
	if err != nil {
		// Misses that are genuinely absent are not stored: negative caching
		// would need its own invalidation on create, and the win is small.
		return product.Product{}, err
	}

	r.lookups.WithLabelValues("miss").Inc()
	r.store(ctx, found)
	return found, nil
}

func (r *Repository) fromCache(ctx context.Context, id string) (product.Product, bool) {
	raw, err := r.rdb.Get(ctx, key(id)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			r.lookups.WithLabelValues("error").Inc()
			r.log.LogAttrs(ctx, slog.LevelWarn, "cache read failed, falling through to the database",
				slog.String("error", err.Error()))
		}
		return product.Product{}, false
	}

	var cached product.Product
	if err := json.Unmarshal(raw, &cached); err != nil {
		// A value that will not decode is worse than no value; drop it so the
		// next read repopulates rather than failing forever.
		r.lookups.WithLabelValues("error").Inc()
		r.drop(ctx, id)
		return product.Product{}, false
	}
	return cached, true
}

func (r *Repository) store(ctx context.Context, p product.Product) {
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := r.rdb.Set(ctx, key(p.ID), raw, r.ttl).Err(); err != nil {
		r.log.LogAttrs(ctx, slog.LevelWarn, "cache write failed",
			slog.String("error", err.Error()))
	}
}

func (r *Repository) drop(ctx context.Context, ids ...string) {
	if len(ids) == 0 {
		return
	}

	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		keys = append(keys, key(id))
	}

	// One round trip for the whole set rather than one per key, which matters
	// when a seller with a large catalogue is renamed.
	if err := r.rdb.Del(ctx, keys...).Err(); err != nil {
		r.log.LogAttrs(ctx, slog.LevelWarn, "cache invalidation failed",
			slog.Int("keys", len(keys)), slog.String("error", err.Error()))
	}
}

// Create writes through the inner repository. There is nothing to invalidate:
// the product did not exist, so nothing can be cached under its ID.
func (r *Repository) Create(ctx context.Context, p product.Product, events []product.OutboxEvent) (product.Product, error) {
	return r.inner.Create(ctx, p, events)
}

// Update invalidates rather than rewrites.
//
// Writing the new value back would be one fewer database read later, but it
// makes two writers racing able to leave the cache holding the older of two
// values indefinitely. Deleting means the next read repopulates from the
// source of truth.
func (r *Repository) Update(ctx context.Context, id string, upd product.Update, events []product.OutboxEvent) (product.Product, error) {
	updated, err := r.inner.Update(ctx, id, upd, events)
	if err != nil {
		return product.Product{}, err
	}
	r.drop(ctx, id)
	return updated, nil
}

func (r *Repository) Delete(ctx context.Context, id string, events []product.OutboxEvent) error {
	if err := r.inner.Delete(ctx, id, events); err != nil {
		return err
	}
	r.drop(ctx, id)
	return nil
}

// List is not cached. A paged, filtered list has a different key for every
// combination of filter and page, and every write to any product would have to
// invalidate an unknown number of them. Caching the individual reads that make
// up a list is the part that pays.
func (r *Repository) List(ctx context.Context, sellerID string, limit, offset int) ([]product.Product, int, error) {
	return r.inner.List(ctx, sellerID, limit, offset)
}

// RenameSeller invalidates exactly the products the rename touched.
//
// This is why the inner repository returns the affected IDs rather than a
// count: the alternative is SCAN-ing Redis for keys that might belong to this
// seller, which is O(keyspace) and the operation most likely to be blamed when
// a cache falls over.
func (r *Repository) RenameSeller(ctx context.Context, sellerID, shopName string) ([]string, error) {
	affected, err := r.inner.RenameSeller(ctx, sellerID, shopName)
	if err != nil {
		return nil, fmt.Errorf("rename seller: %w", err)
	}
	r.drop(ctx, affected...)
	return affected, nil
}

// Reserve takes stock and drops every product it touched from the cache.
//
// Stock is part of the cached document, so a reservation that did not
// invalidate would leave the catalogue advertising units that are already
// spoken for — the one kind of staleness a shopper notices immediately.
func (r *Repository) Reserve(ctx context.Context, key string, items []product.ReserveItem) ([]product.ReservedItem, error) {
	reserved, err := r.inner.Reserve(ctx, key, items)
	if err != nil {
		// Some lines may have been taken and rolled back inside the
		// transaction, so invalidate what was asked for rather than what came
		// back. Dropping a key that was never stale costs one round trip.
		r.drop(ctx, productIDs(items)...)
		return nil, err
	}

	r.drop(ctx, reservedIDs(reserved)...)
	return reserved, nil
}

// Release puts stock back and invalidates the same way.
func (r *Repository) Release(ctx context.Context, key string, items []product.ReserveItem) error {
	if err := r.inner.Release(ctx, key, items); err != nil {
		return err
	}

	// The release path knows the key, not necessarily the items: the inner
	// repository looks them up. Invalidating what the caller named covers the
	// case where it does know, and the TTL covers the rest.
	r.drop(ctx, productIDs(items)...)
	return nil
}

// Confirm changes no field this cache holds, so it passes straight through.
func (r *Repository) Confirm(ctx context.Context, key string) error {
	return r.inner.Confirm(ctx, key)
}

// ReleaseExpired puts stock back, which is a cached field.
//
// The affected product IDs are not known here — the inner repository looks them
// up per reservation — so this relies on the TTL rather than pretending to
// invalidate precisely. It runs rarely and on abandoned carts, so a cache entry
// that is briefly short by a few units is the cheaper wrong answer than
// scanning the keyspace for something to drop.
func (r *Repository) ReleaseExpired(ctx context.Context, olderThan time.Duration) (int, error) {
	return r.inner.ReleaseExpired(ctx, olderThan)
}

func productIDs(items []product.ReserveItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ProductID)
	}
	return ids
}

func reservedIDs(items []product.ReservedItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ProductID)
	}
	return ids
}
