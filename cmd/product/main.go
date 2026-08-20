// Command product runs the Product service: the things sellers list for sale.
//
// It is the consumer in this system. It never calls the Seller service: it
// builds its own copy of the seller facts it needs from that service's events,
// so a listing page renders without a single outbound call.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
	"github.com/DoIttikorn/e-commerce/internal/appserver"
	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/kafka"
	"github.com/DoIttikorn/e-commerce/internal/product"
	productevents "github.com/DoIttikorn/e-commerce/internal/product/events"
	producthandler "github.com/DoIttikorn/e-commerce/internal/product/handler"
	productmongo "github.com/DoIttikorn/e-commerce/internal/product/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/product/rediscache"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := appserver.Signals()
	defer stop()

	app, err := appserver.New(ctx, "product")
	if err != nil {
		return err
	}

	// Required: without the seller stream this service cannot learn who owns
	// what, and every create would fail with an unknown seller.
	if !app.Cfg.Kafka.Enabled() {
		return errors.New("KAFKA_BROKERS is required: this service is fed by seller events")
	}

	mongoRepo := productmongo.NewRepository(app.Mongo)
	if err := mongoRepo.EnsureIndexes(ctx); err != nil {
		return err
	}

	directory := productmongo.NewDirectory(app.Mongo)
	if err := directory.EnsureIndexes(ctx); err != nil {
		return err
	}

	// The cache is a decorator over the same port, so it is genuinely optional:
	// with REDIS_ADDR unset the service runs identically, only slower. That is
	// the difference between a cache and a dependency.
	var repo product.Repository = mongoRepo
	if app.Cfg.Redis.Enabled() {
		rdb, err := database.NewRedis(ctx, app.Cfg.Redis.Addr, app.Cfg.Redis.Password, app.Cfg.Redis.DB)
		if err != nil {
			return err
		}
		app.OnShutdown(func(context.Context) error { return rdb.Close() })

		repo = rediscache.New(mongoRepo, rdb, app.Cfg.Redis.TTL, app.Log, app.Registry)

		// Readiness, not liveness: if Redis goes away the service still serves
		// every request from MongoDB, so it must not be restarted for it.
		app.ReadyCheck("redis", func(ctx context.Context) error { return rdb.Ping(ctx).Err() })
	}

	svc := product.NewService(repo, directory, app.Log)

	consumer := kafka.NewConsumer(
		app.Cfg.Kafka.Brokers, app.Cfg.Kafka.GroupID, sellerv1.TopicSellerEvents, app.Log)
	consumer.Handle(productevents.SellerHandler(svc, app.Log))
	app.Go(consumer.Run)
	app.OnShutdown(func(context.Context) error { return consumer.Close() })

	tokens := auth.NewTokens(app.Cfg.JWTSecret, app.Cfg.JWTTTL)
	producthandler.New(svc, app.Log).Register(app.Router, auth.Middleware(tokens))

	return app.Run(ctx)
}
