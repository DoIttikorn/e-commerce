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
	if err := kafka.EnsureTopic(ctx, app.Cfg.Kafka.Brokers, sellerv1.TopicSellerEvents, 1); err != nil {
		return err
	}

	publisher := kafka.NewPublisher(app.Cfg.Kafka.Brokers, app.Log)
	app.OnShutdown(func(context.Context) error { return publisher.Close() })

	repo := sellermongo.NewRepository(app.Mongo)
	if err := repo.EnsureIndexes(ctx); err != nil {
		return err
	}

	svc := seller.NewService(repo, publisher, app.Log)
	tokens := auth.NewTokens(app.Cfg.JWTSecret, app.Cfg.JWTTTL)
	sellerhandler.New(svc, app.Log).Register(app.Router, auth.Middleware(tokens))

	return app.Run(ctx)
}
