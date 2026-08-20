// Command user runs the User service: identity, credentials, and the accounts
// every other domain refers to.
//
// What this file contains is the wiring that is specific to this service. The
// bootstrap every service shares — config, logging, Mongo, metrics, health,
// graceful shutdown — lives in internal/appserver.
package main

import (
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	userv1 "github.com/DoIttikorn/e-commerce/api/user/v1"
	"github.com/DoIttikorn/e-commerce/internal/appserver"
	"github.com/DoIttikorn/e-commerce/internal/auth"
	"github.com/DoIttikorn/e-commerce/internal/tracing"
	"github.com/DoIttikorn/e-commerce/internal/user"
	usergapi "github.com/DoIttikorn/e-commerce/internal/user/gapi"
	userhandler "github.com/DoIttikorn/e-commerce/internal/user/handler"
	usermongo "github.com/DoIttikorn/e-commerce/internal/user/mongodb"
)

// countInterval is the ten seconds the brief asks for.
const countInterval = 10 * time.Second

func main() {
	// Written to stderr rather than through slog: configuration failures happen
	// before the logger exists, and their multi-line detail survives plain
	// output but not structured escaping.
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := appserver.Signals()
	defer stop()

	app, err := appserver.New(ctx, "user")
	if err != nil {
		return err
	}

	repo := usermongo.NewRepository(app.Mongo)
	// Creating the unique index at startup rather than by migration keeps the
	// guarantee with the code that depends on it. It is idempotent.
	if err := repo.EnsureIndexes(ctx); err != nil {
		return err
	}

	// The only issuer in the system. With a key pair configured it holds the
	// private half and nothing else does, so no other service can mint a token
	// however badly it is compromised.
	tokens, err := auth.NewIssuerFrom(
		app.Cfg.JWTSecret, app.Cfg.JWTPrivateKey, app.Cfg.JWTPublicKey, app.Cfg.JWTTTL)
	if err != nil {
		return err
	}

	// Verification uses the same object here, because this service checks the
	// tokens it issued.
	verifier, err := auth.NewVerifierFrom(app.Cfg.JWTSecret, app.Cfg.JWTPublicKey)
	if err != nil {
		return err
	}

	svc := user.NewService(repo, auth.NewHasher(auth.DefaultCost), tokens)

	// Two driving adapters over one service: the hexagon made concrete.
	userhandler.New(svc, app.Log).Register(app.Router, auth.Middleware(verifier))

	app.GRPC = grpc.NewServer(
		grpc.UnaryInterceptor(usergapi.AuthInterceptor(verifier)),
		tracing.ServerOption(),
	)
	userv1.RegisterUserServiceServer(app.GRPC, usergapi.New(svc))
	// Reflection lets grpcurl discover the surface without a copy of the .proto.
	reflection.Register(app.GRPC)

	app.Go(user.NewCountLogger(svc, app.Log, countInterval).Run)

	return app.Run(ctx)
}
