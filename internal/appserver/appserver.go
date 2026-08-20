// Package appserver is the bootstrap every service binary shares.
//
// It is not part of the house convention, and it exists for one reason: with a
// binary per domain, the configuration loading, logger, Mongo connection,
// metrics, health endpoints, and graceful shutdown are identical in every
// main.go. Copying two hundred lines per service means a fix to shutdown needs
// three edits and gets two.
//
// What varies per service — which domain is wired, which events it consumes —
// stays in that service's main.go, which is the part worth reading.
package appserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"google.golang.org/grpc"

	"github.com/DoIttikorn/e-commerce/internal/admin"
	"github.com/DoIttikorn/e-commerce/internal/config"
	"github.com/DoIttikorn/e-commerce/internal/database"
	"github.com/DoIttikorn/e-commerce/internal/logging"
	"github.com/DoIttikorn/e-commerce/internal/middleware"
	"github.com/DoIttikorn/e-commerce/internal/router"
	"github.com/DoIttikorn/e-commerce/internal/router/chirouter"
	"github.com/DoIttikorn/e-commerce/internal/tracing"
)

const (
	startupTimeout   = 15 * time.Second
	readinessTimeout = 2 * time.Second
)

// App is one service: its configuration, dependencies, and servers.
type App struct {
	Name     string
	Cfg      config.Config
	Log      *slog.Logger
	Mongo    *mongo.Database
	Registry *prometheus.Registry
	Router   router.Router

	// GRPC is nil unless a service sets it before Run. Services that expose no
	// RPCs simply leave it alone and no listener is opened.
	GRPC *grpc.Server

	readyChecks map[string]func(context.Context) error
	tasks       []func(context.Context)
	closers     []func(context.Context) error
}

// New loads configuration, connects to MongoDB, and mounts the observability
// and health surface. The caller wires its domain onto App.Router and calls Run.
func New(ctx context.Context, name string) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	log := logging.New(os.Stdout, cfg.LogLevel).With(slog.String("service", name))
	slog.SetDefault(log)

	startupCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	// Before anything else that might want to be traced. With no collector
	// configured this installs a no-op provider and the W3C propagator, so the
	// service still passes through a trace context it did not start.
	flushTraces, err := tracing.Init(startupCtx, tracing.Config{
		Endpoint:    cfg.Tracing.Endpoint,
		ServiceName: name,
		SampleRatio: cfg.Tracing.SampleRatio,
	})
	if err != nil {
		return nil, err
	}

	db, err := database.NewMongo(startupCtx, cfg.Mongo.URI, cfg.Mongo.Database)
	if err != nil {
		return nil, err
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	app := &App{
		Name:        name,
		Cfg:         cfg,
		Log:         log,
		Mongo:       db,
		Registry:    registry,
		Router:      chirouter.New(),
		readyChecks: map[string]func(context.Context) error{},
	}

	// Order matters: RequestID first, so the metrics, spans, and log lines for
	// one request all carry the same correlation IDs. Tracing goes second so
	// the trace ID is on the request context before anything logs.
	app.Router.Use(
		middleware.RequestID(),
		middleware.Tracing(name),
		middleware.NewMetrics(registry).Middleware(),
		middleware.Logging(log),
	)
	app.Router.Handle(http.MethodGet, "/healthz", liveness)
	app.Router.Handle(http.MethodGet, "/readyz", app.readiness)

	app.ReadyCheck("mongo", func(ctx context.Context) error {
		return db.Client().Ping(ctx, readpref.Primary())
	})
	app.OnShutdown(func(ctx context.Context) error { return db.Client().Disconnect(ctx) })

	// Registered before Mongo's closer and therefore run after it: cleanups run
	// in reverse, and a span recorded during shutdown should still be exported.
	app.OnShutdown(flushTraces)

	log.LogAttrs(ctx, slog.LevelInfo, "connected to mongo",
		slog.String("database", cfg.Mongo.Database))

	// Logged for every service, whether or not it uses them: a half-configured
	// environment — a product service that quietly started without Redis, say —
	// is otherwise only visible as a performance mystery later.
	log.LogAttrs(ctx, slog.LevelInfo, "optional infrastructure",
		slog.Bool("redis_configured", cfg.Redis.Enabled()),
		slog.Bool("kafka_configured", cfg.Kafka.Enabled()),
		slog.Bool("tracing_configured", cfg.Tracing.Enabled()),
	)

	return app, nil
}

// ReadyCheck registers a dependency probe. Every registered check must pass for
// /readyz to answer 200.
func (a *App) ReadyCheck(name string, check func(context.Context) error) {
	a.readyChecks[name] = check
}

// Go registers a background loop. It is started by Run and must return when its
// context is cancelled; Run waits for all of them before releasing resources.
func (a *App) Go(task func(context.Context)) {
	a.tasks = append(a.tasks, task)
}

// OnShutdown registers a cleanup, run after the servers stop and the background
// tasks have finished. Cleanups run in reverse order of registration.
func (a *App) OnShutdown(closer func(context.Context) error) {
	a.closers = append(a.closers, closer)
}

// Run starts the servers and blocks until a signal arrives or a server fails.
func (a *App) Run(ctx context.Context) error {
	apiSrv := &http.Server{
		Addr:              a.Cfg.HTTPAddr,
		Handler:           a.Router.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	adminSrv := &http.Server{
		Addr:              a.Cfg.AdminAddr,
		Handler:           admin.NewHandler(a.Registry),
		ReadHeaderTimeout: 10 * time.Second,
	}

	var background sync.WaitGroup
	for _, task := range a.tasks {
		background.Add(1)
		go func() {
			defer background.Done()
			task(ctx)
		}()
	}

	serveErr := make(chan error, 3)
	go a.serve(ctx, "api", apiSrv, serveErr)
	go a.serve(ctx, "admin", adminSrv, serveErr)

	var grpcListener net.Listener
	if a.GRPC != nil {
		listener, err := net.Listen("tcp", a.Cfg.GRPCAddr)
		if err != nil {
			return fmt.Errorf("grpc listen: %w", err)
		}
		grpcListener = listener

		go func() {
			a.Log.LogAttrs(ctx, slog.LevelInfo, "server listening",
				slog.String("server", "grpc"), slog.String("addr", a.Cfg.GRPCAddr))
			if err := a.GRPC.Serve(grpcListener); err != nil {
				serveErr <- fmt.Errorf("grpc serve: %w", err)
			}
		}()
	}

	var runErr error
	select {
	case runErr = <-serveErr:
	case <-ctx.Done():
		a.Log.LogAttrs(context.WithoutCancel(ctx), slog.LevelInfo, "shutting down")
	}

	// ctx is cancelled by now, so shutdown needs a live one of its own.
	shutdownCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), a.Cfg.ShutdownTimeout)
	defer cancel()

	// API first, so the last requests are still measurable while the metrics
	// endpoint is up to be scraped one final time.
	if err := apiSrv.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("api shutdown: %w", err))
	}
	if a.GRPC != nil {
		a.GRPC.GracefulStop()
	}
	if err := adminSrv.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("admin shutdown: %w", err))
	}

	// Background tasks finish before the closers run, so nothing is mid-query
	// when its client is disconnected underneath it.
	background.Wait()

	for i := len(a.closers) - 1; i >= 0; i-- {
		if err := a.closers[i](shutdownCtx); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return runErr
}

func (a *App) serve(ctx context.Context, name string, srv *http.Server, errCh chan<- error) {
	a.Log.LogAttrs(ctx, slog.LevelInfo, "server listening",
		slog.String("server", name), slog.String("addr", srv.Addr))

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s serve: %w", name, err)
	}
}

// liveness answers whether the process is running, and checks no dependencies.
//
// A failing Kubernetes liveness probe restarts the pod, so a database check
// here would turn a brief outage into a restart loop across every instance at
// once — an outage where there was only degradation.
func liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, `{"status":"ok"}`)
}

// readiness answers whether this instance can serve traffic right now. A
// failure removes it from the load balancer and leaves it running to recover.
func (a *App) readiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	for name, check := range a.readyChecks {
		if err := check(ctx); err != nil {
			a.Log.LogAttrs(ctx, slog.LevelWarn, "readiness check failed",
				slog.String("dependency", name), slog.String("error", err.Error()))
			writeJSON(w, http.StatusServiceUnavailable,
				fmt.Sprintf(`{"status":"unavailable","dependency":%q}`, name))
			return
		}
	}
	writeJSON(w, http.StatusOK, `{"status":"ready"}`)
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// Signals returns a context cancelled on SIGINT or SIGTERM, and the stop
// function to release the handler.
func Signals() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
