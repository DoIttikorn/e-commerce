// Command api runs the user management service.
//
// This file is the only place that knows which concrete adapters are in use.
// It loads configuration, builds dependencies, starts the servers, and shuts
// them down cleanly.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/DoIttikorn/e-commerce/internal/config"
	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/middleware"
	"github.com/DoIttikorn/e-commerce/internal/router/chirouter"
)

// startupTimeout bounds how long the service waits for its dependencies
// before giving up and exiting.
const startupTimeout = 15 * time.Second

func main() {
	// Written to stderr rather than through slog: configuration failures
	// happen before the logger exists, and their multi-line detail survives
	// plain output but not structured escaping.
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "startup failed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	// ctx is cancelled on SIGINT/SIGTERM and drives shutdown of everything below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	db, err := database.NewMongo(startupCtx, cfg.MongoURI, cfg.MongoDatabase)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Client().Disconnect(context.WithoutCancel(ctx)); err != nil {
			log.LogAttrs(context.WithoutCancel(ctx), slog.LevelError, "mongo disconnect",
				slog.String("error", err.Error()))
		}
	}()
	log.LogAttrs(ctx, slog.LevelInfo, "connected to mongo",
		slog.String("database", cfg.MongoDatabase))

	r := chirouter.New()
	r.Use(middleware.Logging(log))
	r.Handle(http.MethodGet, "/healthz", healthz(db))

	// Domain wiring goes here once the user domain exists:
	//   repo := usermongodb.NewRepository(db)
	//   svc  := user.NewService(repo, tokens)
	//   userhandler.New(svc).Register(r)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// ListenAndServe blocks, so it runs in its own goroutine and reports a
	// startup failure (a taken port, for instance) back through serveErr.
	serveErr := make(chan error, 1)
	go func() {
		log.LogAttrs(ctx, slog.LevelInfo, "http server listening",
			slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("http serve: %w", err)
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.LogAttrs(context.WithoutCancel(ctx), slog.LevelInfo, "shutting down")
	}

	// Shutdown needs a live context: ctx is already cancelled at this point.
	shutdownCtx, cancelShutdown := context.WithTimeout(
		context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

// healthz reports whether the service can still reach MongoDB.
func healthz(db *mongo.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")
		if err := db.Client().Ping(ctx, readpref.Primary()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}
