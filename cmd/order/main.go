// Command order runs the Order service.
//
// It is the only service that calls another synchronously: reserving stock has
// to happen before a buyer is told their order exists. Everything else it says
// to the rest of the system goes out through the outbox.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/DoIttikorn/e-commerce/internal/appserver"
	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/kafka"
	"github.com/DoIttikorn/e-commerce/internal/order"
	"github.com/DoIttikorn/e-commerce/internal/order/grpcstock"
	orderhandler "github.com/DoIttikorn/e-commerce/internal/order/handler"
	ordermongo "github.com/DoIttikorn/e-commerce/internal/order/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/outbox"

	orderv1 "github.com/DoIttikorn/e-commerce/api/order/v1"
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

	app, err := appserver.New(ctx, "order")
	if err != nil {
		return err
	}

	if !app.Cfg.Kafka.Enabled() {
		return errors.New("KAFKA_BROKERS is required: this service publishes what it did")
	}
	if app.Cfg.ProductGRPCAddr == "" {
		return errors.New("PRODUCT_GRPC_ADDR is required: an order cannot be placed without reserving stock")
	}

	if err := kafka.EnsureTopic(ctx, app.Cfg.Kafka.Brokers, orderv1.TopicOrderEvents, 1); err != nil {
		return err
	}

	publisher := kafka.NewPublisher(app.Cfg.Kafka.Brokers, app.Log)
	app.OnShutdown(func(context.Context) error { return publisher.Close() })

	// Lazy: this returns before a connection exists and reconnects on its own,
	// so a Product service that is slow to start does not stop this one.
	stockClient, err := grpcstock.Dial(app.Cfg.ProductGRPCAddr)
	if err != nil {
		return err
	}
	app.OnShutdown(func(context.Context) error { return stockClient.Close() })

	repo := ordermongo.NewRepository(app.Mongo)
	if err := repo.EnsureIndexes(ctx); err != nil {
		return err
	}

	svc := order.NewService(repo, stockClient, app.Log)

	tokens := auth.NewTokens(app.Cfg.JWTSecret, app.Cfg.JWTTTL)
	orderhandler.New(svc, app.Log).Register(app.Router, auth.Middleware(tokens))

	// The relay is what turns rows in the outbox into messages on the broker.
	// It runs in this process rather than as its own deployment because it is
	// coupled to this database and nothing else, and a separate binary would
	// be one more thing to notice had stopped.
	outboxColl := app.Mongo.Collection(ordermongo.OutboxName)
	app.Go(outbox.NewRelay(outboxColl, publisher, app.Log).Run)

	// A relay that has stopped looks exactly like an idle one until this grows,
	// so it is worth being able to see rather than infer.
	app.ReadyCheck("outbox", func(ctx context.Context) error {
		_, err := outbox.PendingCount(ctx, outboxColl)
		return err
	})

	return app.Run(ctx)
}
