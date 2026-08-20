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

	tokens := auth.NewTokens(app.Cfg.JWTSecret, app.Cfg.JWTTTL)
	svc := user.NewService(repo, auth.NewHasher(auth.DefaultCost), tokens)

	// Two driving adapters over one service: the hexagon made concrete.
	userhandler.New(svc, app.Log).Register(app.Router, auth.Middleware(tokens))

	app.GRPC = grpc.NewServer(grpc.UnaryInterceptor(usergapi.AuthInterceptor(tokens)))
	userv1.RegisterUserServiceServer(app.GRPC, usergapi.New(svc))
	// Reflection lets grpcurl discover the surface without a copy of the .proto.
	reflection.Register(app.GRPC)

	app.Go(user.NewCountLogger(svc, app.Log, countInterval).Run)

	return app.Run(ctx)
}
