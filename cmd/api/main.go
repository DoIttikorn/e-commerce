// Command api runs the e-commerce service.
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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"github.com/DoIttikorn/e-commerce/internal/admin"
	"github.com/DoIttikorn/e-commerce/internal/config"
	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/logging"
	"github.com/DoIttikorn/e-commerce/internal/middleware"
	"github.com/DoIttikorn/e-commerce/internal/router/chirouter"
)

const (
	// startupTimeout bounds how long the service waits for its dependencies
	// before giving up and exiting.
	startupTimeout = 15 * time.Second

	// readinessTimeout keeps a probe from hanging on a wedged dependency.
	readinessTimeout = 2 * time.Second
)

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

	log := logging.New(os.Stdout, cfg.LogLevel)
	slog.SetDefault(log)

	// ctx is cancelled on SIGINT/SIGTERM and drives shutdown of everything below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()

	db, err := database.NewMongo(startupCtx, cfg.Mongo.URI, cfg.Mongo.Database)
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
		slog.String("database", cfg.Mongo.Database))

	// Redis and Kafka are configured but have no clients yet: they are wired
	// when a domain needs caching, locking, or events. Logging their state at
	// startup makes a half-configured environment obvious immediately.
	log.LogAttrs(ctx, slog.LevelInfo, "optional infrastructure",
		slog.Bool("redis_configured", cfg.Redis.Enabled()),
		slog.Bool("kafka_configured", cfg.Kafka.Enabled()),
	)

	// A dedicated registry rather than the package-level default: no global
	// state, and the Go and process collectors are registered where they can
	// be seen, since runtime metrics are most of what performance work needs.
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	metrics := middleware.NewMetrics(registry)

	r := chirouter.New()
	// Order matters: RequestID first so the metrics and log lines for a request
	// carry the same correlation ID.
	r.Use(
		middleware.RequestID(),
		metrics.Middleware(),
		middleware.Logging(log),
	)
	r.Handle(http.MethodGet, "/healthz", liveness)
	r.Handle(http.MethodGet, "/readyz", readiness(db))

	// Domain wiring goes here once a domain exists:
	//   repo := usermongodb.NewRepository(db)
	//   svc  := user.NewService(repo, tokens)
	//   userhandler.New(svc).Register(r)

	apiSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           r.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	adminSrv := &http.Server{
		Addr:              cfg.AdminAddr,
		Handler:           admin.NewHandler(registry),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Buffered for both servers so neither goroutine leaks if the other fails first.
	serveErr := make(chan error, 2)
	go serve(ctx, log, "api", apiSrv, serveErr)
	go serve(ctx, log, "admin", adminSrv, serveErr)

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

	// The API drains first so the last requests are still being measured while
	// the metrics endpoint is up to be scraped one final time.
	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("api shutdown: %w", err)
	}
	if err := adminSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("admin shutdown: %w", err)
	}
	return nil
}

func serve(ctx context.Context, log *slog.Logger, name string, srv *http.Server, errCh chan<- error) {
	log.LogAttrs(ctx, slog.LevelInfo, "server listening",
		slog.String("server", name), slog.String("addr", srv.Addr))

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s serve: %w", name, err)
	}
}

// liveness answers whether the process is running, and deliberately checks no
// dependencies.
//
// A failing Kubernetes liveness probe restarts the pod, so checking MongoDB
// here would turn a brief database blip into a restart loop across every
// instance at once — taking down a service that was only degraded.
func liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, `{"status":"ok"}`)
}

// readiness answers whether this instance can serve traffic right now.
//
// A failure here removes the instance from the load balancer and leaves it
// running, which is the right response to a dependency being briefly away: it
// recovers on its own once MongoDB is reachable again.
func readiness(db *mongo.Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()

		if err := db.Client().Ping(ctx, readpref.Primary()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				`{"status":"unavailable","dependency":"mongo"}`)
			return
		}
		writeJSON(w, http.StatusOK, `{"status":"ready"}`)
	}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
