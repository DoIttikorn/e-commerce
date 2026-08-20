// Command marketplace runs the Marketplace service: one searchable view of
// what every shop is selling.
//
// It owns no truth and has no write API. Everything in its database arrived as
// an event from somewhere else, which is why this file is mostly three
// consumers and one HTTP route.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	orderv1 "github.com/DoIttikorn/e-commerce/api/order/v1"
	productv1 "github.com/DoIttikorn/e-commerce/api/product/v1"
	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
	"github.com/DoIttikorn/e-commerce/internal/appserver"
	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/kafka"
	"github.com/DoIttikorn/e-commerce/internal/marketplace"
	marketplaceevents "github.com/DoIttikorn/e-commerce/internal/marketplace/events"
	marketplacehandler "github.com/DoIttikorn/e-commerce/internal/marketplace/handler"
	marketplacemongo "github.com/DoIttikorn/e-commerce/internal/marketplace/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/marketplace/rediscache"
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

	app, err := appserver.New(ctx, "marketplace")
	if err != nil {
		return err
	}

	if !app.Cfg.Kafka.Enabled() {
		return errors.New("KAFKA_BROKERS is required: this service is built entirely from events")
	}

	mongoRepo := marketplacemongo.NewRepository(app.Mongo)
	if err := mongoRepo.EnsureIndexes(ctx); err != nil {
		return err
	}

	// The cache is optional in the same way the Product one is: without
	// REDIS_ADDR the service behaves identically, only slower.
	var repo marketplace.Repository = mongoRepo
	if app.Cfg.Redis.Enabled() {
		rdb, err := database.NewRedis(ctx, app.Cfg.Redis.Addr, app.Cfg.Redis.Password, app.Cfg.Redis.DB)
		if err != nil {
			return err
		}
		app.OnShutdown(func(context.Context) error { return rdb.Close() })

		cache := rediscache.New(mongoRepo, rdb, app.Cfg.Redis.TTL, app.Log, app.Registry)
		repo = cache
		app.ReadyCheck("redis", cache.Ping)
	}

	svc := marketplace.NewService(repo, app.Log)
	marketplacehandler.New(svc, app.Log).Register(app.Router, nil)

	// Three streams, three consumer groups.
	//
	// Separate groups rather than one, so a slow or failing stream cannot hold
	// up the others: a backlog of order events must not stop new products from
	// appearing in search.
	subscriptions := []struct {
		topic   string
		group   string
		handler func(ctx context.Context, key, value []byte) error
	}{
		{productv1.TopicProductEvents, app.Cfg.Kafka.GroupID + "-product", marketplaceevents.ProductHandler(svc, app.Log)},
		{sellerv1.TopicSellerEvents, app.Cfg.Kafka.GroupID + "-seller", marketplaceevents.SellerHandler(svc, app.Log)},
		{orderv1.TopicOrderEvents, app.Cfg.Kafka.GroupID + "-order", marketplaceevents.OrderHandler(svc, app.Log)},
	}

	for _, sub := range subscriptions {
		if err := kafka.EnsureTopic(ctx, app.Cfg.Kafka.Brokers, sub.topic, app.Cfg.Kafka.Partitions); err != nil {
			return err
		}

		consumer := kafka.NewConsumer(app.Cfg.Kafka.Brokers, sub.group, sub.topic, app.Log)
		consumer.Handle(sub.handler)
		app.Go(consumer.Run)
		app.OnShutdown(func(context.Context) error { return consumer.Close() })
	}

	return app.Run(ctx)
}
