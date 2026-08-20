// Command seller runs the Seller service: the shops that own products.
//
// It is the publisher in this system. Nothing calls it to ask who a seller is;
// it announces changes, and the services that care keep their own copy.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
	"github.com/DoIttikorn/e-commerce/internal/appserver"
	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/kafka"
	"github.com/DoIttikorn/e-commerce/internal/outbox"
	"github.com/DoIttikorn/e-commerce/internal/seller"
	sellerhandler "github.com/DoIttikorn/e-commerce/internal/seller/handler"
	sellermongo "github.com/DoIttikorn/e-commerce/internal/seller/mongodb"
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

	app, err := appserver.New(ctx, "seller")
	if err != nil {
		return err
	}

	// Kafka is optional to the platform but required by this service: a Seller
	// that cannot announce a rename leaves every consumer holding a stale copy.
	// Saying so at startup beats discovering it on the first write.
	if !app.Cfg.Kafka.Enabled() {
		return errors.New("KAFKA_BROKERS is required: this service publishes events others depend on")
	}

	// Created up front rather than left to auto-creation on first publish, so a
	// consumer that starts before the first seller is registered has a topic to
	// attach to instead of waiting for one to appear.
	if err := kafka.EnsureTopic(ctx, app.Cfg.Kafka.Brokers, sellerv1.TopicSellerEvents, app.Cfg.Kafka.Partitions); err != nil {
		return err
	}

	publisher := kafka.NewPublisher(app.Cfg.Kafka.Brokers, app.Log)
	app.OnShutdown(func(context.Context) error { return publisher.Close() })

	repo := sellermongo.NewRepository(app.Mongo)
	if err := repo.EnsureIndexes(ctx); err != nil {
		return err
	}

	svc := seller.NewService(repo, app.Log)

	// The service records events; the relay publishes them. Nothing in the
	// request path talks to Kafka, so a broker outage slows nothing and loses
	// nothing — the events simply queue in MongoDB until it returns.
	outboxColl := app.Mongo.Collection(sellermongo.OutboxName)
	app.Go(outbox.NewRelay(outboxColl, publisher, app.Log).Run)

	app.ReadyCheck("outbox", func(ctx context.Context) error {
		_, err := outbox.PendingCount(ctx, outboxColl)
		return err
	})
	// Verify only. This service never issues a token, so it never needs a
	// signing key — and with a key pair configured it could not sign one if it
	// tried, which is the point of splitting them.
	verifier, err := auth.NewVerifierFrom(app.Cfg.JWTSecret, app.Cfg.JWTPublicKey)
	if err != nil {
		return err
	}
	sellerhandler.New(svc, app.Log).Register(app.Router, auth.Middleware(verifier))

	return app.Run(ctx)
}
