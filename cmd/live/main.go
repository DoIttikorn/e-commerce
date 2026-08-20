// Command live runs the Live Commerce service.
//
// It is the only service here that holds connections rather than answering
// requests, and that is what shapes it: Redis is not an optimisation but a
// requirement, because a viewer count and a broadcast both have to be true
// across every instance rather than within one.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	orderv1 "github.com/DoIttikorn/e-commerce/api/order/v1"
	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
	"github.com/DoIttikorn/e-commerce/internal/appserver"
	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/kafka"
	"github.com/DoIttikorn/e-commerce/internal/live"
	liveevents "github.com/DoIttikorn/e-commerce/internal/live/events"
	livehandler "github.com/DoIttikorn/e-commerce/internal/live/handler"
	livemongo "github.com/DoIttikorn/e-commerce/internal/live/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/live/redisbus"
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

	app, err := appserver.New(ctx, "live")
	if err != nil {
		return err
	}

	if !app.Cfg.Kafka.Enabled() {
		return errors.New("KAFKA_BROKERS is required: this service is fed by seller and order events")
	}
	// Redis is required here, unlike in product and marketplace where it only
	// makes things faster. Without it a second instance of this service would
	// have its own viewer count and its own idea of who to broadcast to, and
	// both would be wrong.
	if !app.Cfg.Redis.Enabled() {
		return errors.New("REDIS_ADDR is required: presence and broadcast are shared state, not a cache")
	}

	rdb, err := database.NewRedis(ctx, app.Cfg.Redis.Addr, app.Cfg.Redis.Password, app.Cfg.Redis.DB)
	if err != nil {
		return err
	}
	app.OnShutdown(func(context.Context) error { return rdb.Close() })

	bus := redisbus.New(rdb, app.Log)
	// Liveness would be wrong: losing Redis breaks this service's function, but
	// restarting the process does not bring Redis back, and it would drop every
	// connected viewer on the way past.
	app.ReadyCheck("redis", bus.Ping)

	repo := livemongo.NewRepository(app.Mongo)
	if err := repo.EnsureIndexes(ctx); err != nil {
		return err
	}
	directory := livemongo.NewDirectory(app.Mongo)
	if err := directory.EnsureIndexes(ctx); err != nil {
		return err
	}

	svc := live.NewService(repo, directory, bus, bus, app.Log)

	tokens := auth.NewTokens(app.Cfg.JWTSecret, app.Cfg.JWTTTL)
	livehandler.New(svc, bus, app.Log).Register(app.Router, auth.Middleware(tokens))

	subscriptions := []struct {
		topic   string
		group   string
		handler func(ctx context.Context, key, value []byte) error
	}{
		{sellerv1.TopicSellerEvents, app.Cfg.Kafka.GroupID + "-seller", liveevents.SellerHandler(svc, app.Log)},
		{orderv1.TopicOrderEvents, app.Cfg.Kafka.GroupID + "-order", liveevents.OrderHandler(svc, app.Log)},
	}

	for _, sub := range subscriptions {
		if err := kafka.EnsureTopic(ctx, app.Cfg.Kafka.Brokers, sub.topic, 1); err != nil {
			return err
		}

		consumer := kafka.NewConsumer(app.Cfg.Kafka.Brokers, sub.group, sub.topic, app.Log)
		consumer.Handle(sub.handler)
		app.Go(consumer.Run)
		app.OnShutdown(func(context.Context) error { return consumer.Close() })
	}

	return app.Run(ctx)
}
