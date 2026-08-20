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
	"log/slog"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	productv1 "github.com/DoIttikorn/e-commerce/api/product/v1"
	sellerv1 "github.com/DoIttikorn/e-commerce/api/seller/v1"
	"github.com/DoIttikorn/e-commerce/internal/appserver"
	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/kafka"
	"github.com/DoIttikorn/e-commerce/internal/outbox"
	"github.com/DoIttikorn/e-commerce/internal/product"
	productevents "github.com/DoIttikorn/e-commerce/internal/product/events"
	productgapi "github.com/DoIttikorn/e-commerce/internal/product/gapi"
	producthandler "github.com/DoIttikorn/e-commerce/internal/product/handler"
	productmongo "github.com/DoIttikorn/e-commerce/internal/product/mongodb"
	"github.com/DoIttikorn/e-commerce/internal/product/rediscache"
	"github.com/DoIttikorn/e-commerce/internal/servicetls"
	"github.com/DoIttikorn/e-commerce/internal/tracing"
)

const (
	// reaperInterval is how often to look for abandoned reservations. Rarely:
	// this is a cleanup, not a hot path.
	reaperInterval = time.Minute

	// reservationGrace is how long a reservation may stay unconfirmed before
	// its stock is taken back. Long enough that a slow order write is never
	// mistaken for a dead one.
	reservationGrace = 15 * time.Minute
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		os.Exit(1)
	}
}

// stockServerOptions returns mutual TLS when it is configured, and says so
// loudly when it is not.
//
// Failing to start without certificates would make every test and every
// single-machine run need a CA; starting silently without them would make an
// unauthenticated internal port look configured. A warning is the honest
// middle, and it is the line somebody greps for when they wonder why anything
// on the network can reserve stock.
func stockServerOptions(app *appserver.App) ([]grpc.ServerOption, error) {
	// Tracing is unconditional: it is a no-op when no collector is configured,
	// and this is the hop where a trace is most likely to be lost.
	options := []grpc.ServerOption{tracing.ServerOption()}

	if app.Cfg.GRPCTLSDir == "" {
		app.Log.LogAttrs(context.Background(), slog.LevelWarn,
			"gRPC stock port is running WITHOUT mutual TLS; any caller that can reach it may reserve stock",
			slog.String("fix", "set GRPC_TLS_DIR"))
		return options, nil
	}

	creds, err := servicetls.ServerCredentials(app.Cfg.GRPCTLSDir)
	if err != nil {
		return nil, err
	}

	app.Log.LogAttrs(context.Background(), slog.LevelInfo,
		"gRPC stock port requires a client certificate")
	return append(options, grpc.Creds(creds)), nil
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

	publisher := kafka.NewPublisher(app.Cfg.Kafka.Brokers, app.Log)
	app.OnShutdown(func(context.Context) error { return publisher.Close() })

	// This service both consumes and publishes: it learns about sellers from
	// their events, and announces its own catalogue for the Marketplace to index.
	if err := kafka.EnsureTopic(ctx, app.Cfg.Kafka.Brokers, productv1.TopicProductEvents, app.Cfg.Kafka.Partitions); err != nil {
		return err
	}

	svc := product.NewService(repo, directory, app.Log)

	consumer := kafka.NewConsumer(
		app.Cfg.Kafka.Brokers, app.Cfg.Kafka.GroupID, sellerv1.TopicSellerEvents, app.Log)
	consumer.Handle(productevents.SellerHandler(svc, app.Log))
	app.Go(consumer.Run)
	app.OnShutdown(func(context.Context) error { return consumer.Close() })

	// Verify only. This service never issues a token, so it never needs a
	// signing key — and with a key pair configured it could not sign one if it
	// tried, which is the point of splitting them.
	verifier, err := auth.NewVerifierFrom(app.Cfg.JWTSecret, app.Cfg.JWTPublicKey)
	if err != nil {
		return err
	}
	producthandler.New(svc, app.Log).Register(app.Router, auth.Middleware(verifier))

	// Stock reservation is gRPC and internal only: the Order service is the
	// sole caller, and compose exposes this port inside the network without
	// publishing it. Authentication is mutual TLS rather than a bearer token,
	// because there is no user in this call — the question is which service is
	// asking, and only a client certificate answers it.
	grpcOptions, err := stockServerOptions(app)
	if err != nil {
		return err
	}
	app.GRPC = grpc.NewServer(grpcOptions...)
	productv1.RegisterStockServiceServer(app.GRPC, productgapi.New(svc))
	reflection.Register(app.GRPC)

	// The reaper reclaims stock from reservations nobody confirmed.
	//
	// It is the last line of defence in the ordering saga: the Order service
	// confirms a reservation once the order is written, so anything still
	// unconfirmed after the grace period belongs to a caller that died. The
	// period is generous on purpose — reclaiming stock from an order that was
	// actually placed is a far worse failure than holding it a little longer.
	app.Go(func(ctx context.Context) {
		ticker := time.NewTicker(reaperInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := svc.ReleaseExpired(ctx, reservationGrace); err != nil {
					app.Log.LogAttrs(ctx, slog.LevelError, "reaping reservations failed",
						slog.String("error", err.Error()))
				}
			}
		}
	})

	// The service records events; the relay publishes them. Nothing in the
	// request path talks to Kafka.
	outboxColl := app.Mongo.Collection(productmongo.OutboxName)
	app.Go(outbox.NewRelay(outboxColl, publisher, app.Log).Run)

	app.ReadyCheck("outbox", func(ctx context.Context) error {
		_, err := outbox.PendingCount(ctx, outboxColl)
		return err
	})

	return app.Run(ctx)
}
