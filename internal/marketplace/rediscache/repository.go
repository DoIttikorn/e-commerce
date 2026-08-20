// Package rediscache caches search results.
//
// It is deliberately a different strategy from the one in
// internal/product/rediscache, and the difference is the interesting part.
//
// A product is cached by ID and invalidated on write, because the key is known:
// updating product X drops product X. A search result has no such key. Which
// cached queries contain product X is unanswerable without keeping an index
// from every query to every product it returned — which is a second cache, with
// its own invalidation problem, to serve the first.
//
// So this one expires instead. The staleness is bounded by the TTL and stated
// rather than eliminated: for a few seconds after a price change, a search may
// still show the old one. For a catalogue that is an acceptable trade; for
// stock it would not be, which is why the product page reads through the other
// cache and not this one.
package rediscache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/DoIttikorn/e-commerce/internal/marketplace"
)

// keyPrefix is versioned so a change to the cached shape retires the old
// entries rather than decoding them into the new one.
const keyPrefix = "search:v1:"

type cached struct {
	Listings []marketplace.Listing `json:"listings"`
	Total    int                   `json:"total"`
}

// Repository decorates a marketplace.Repository with a short-lived search cache.
type Repository struct {
	inner marketplace.Repository
	rdb   *redis.Client
	ttl   time.Duration
	log   *slog.Logger

	lookups *prometheus.CounterVec
}

var _ marketplace.Repository = (*Repository)(nil)

// New returns a caching Repository.
func New(
	inner marketplace.Repository,
	rdb *redis.Client,
	ttl time.Duration,
	log *slog.Logger,
	reg prometheus.Registerer,
) *Repository {
	lookups := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "marketplace_search_cache_lookups_total",
		Help: "Marketplace search cache lookups by outcome.",
	}, []string{"result"})
	reg.MustRegister(lookups)

	return &Repository{inner: inner, rdb: rdb, ttl: ttl, log: log, lookups: lookups}
}

// key hashes the whole query.
//
// Hashed rather than concatenated because a search term is user input: it can
// contain anything, including the separator, and two different queries that
// serialise to the same string would serve each other's results.
func key(q marketplace.Query) string {
	raw, err := json.Marshal(q)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return keyPrefix + hex.EncodeToString(sum[:])
}

// Search serves from the cache when it can.
//
// A Redis failure falls through to MongoDB. The cache makes search cheaper, not
// possible; a marketplace that stops answering because its cache is unwell has
// traded an outage for a latency improvement.
func (r *Repository) Search(ctx context.Context, q marketplace.Query) ([]marketplace.Listing, int, error) {
	k := key(q)
	if k == "" {
		return r.inner.Search(ctx, q)
	}

	if hit, ok := r.fromCache(ctx, k); ok {
		r.lookups.WithLabelValues("hit").Inc()
		return hit.Listings, hit.Total, nil
	}

	listings, total, err := r.inner.Search(ctx, q)
	if err != nil {
		return nil, 0, err
	}

	r.lookups.WithLabelValues("miss").Inc()
	r.store(ctx, k, cached{Listings: listings, Total: total})
	return listings, total, nil
}

func (r *Repository) fromCache(ctx context.Context, k string) (cached, bool) {
	raw, err := r.rdb.Get(ctx, k).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			r.lookups.WithLabelValues("error").Inc()
			r.log.LogAttrs(ctx, slog.LevelWarn, "search cache read failed, falling through",
				slog.String("error", err.Error()))
		}
		return cached{}, false
	}

	var out cached
	if err := json.Unmarshal(raw, &out); err != nil {
		r.lookups.WithLabelValues("error").Inc()
		_ = r.rdb.Del(ctx, k).Err()
		return cached{}, false
	}
	return out, true
}

func (r *Repository) store(ctx context.Context, k string, value cached) {
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	if err := r.rdb.Set(ctx, k, raw, r.ttl).Err(); err != nil {
		r.log.LogAttrs(ctx, slog.LevelWarn, "search cache write failed",
			slog.String("error", err.Error()))
	}
}

// The write paths pass straight through. They deliberately do not invalidate:
// see the package comment for why there is nothing here to invalidate by.

func (r *Repository) UpsertListing(ctx context.Context, l marketplace.Listing) error {
	return r.inner.UpsertListing(ctx, l)
}

func (r *Repository) RemoveListing(ctx context.Context, productID string) error {
	return r.inner.RemoveListing(ctx, productID)
}

func (r *Repository) ApplySellerChange(ctx context.Context, sellerID, shopName string, active bool) (int64, error) {
	return r.inner.ApplySellerChange(ctx, sellerID, shopName, active)
}

func (r *Repository) RecordSale(ctx context.Context, orderID string, lines []marketplace.SoldLine) error {
	return r.inner.RecordSale(ctx, orderID, lines)
}

// Ping exposes the cache's health so a service can make it a readiness check
// without importing the Redis client itself.
func (r *Repository) Ping(ctx context.Context) error { return r.rdb.Ping(ctx).Err() }
