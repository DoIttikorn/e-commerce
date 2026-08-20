// Package config loads process configuration from the environment.
//
// Load is called once from main and nothing else in the codebase reads
// os.Getenv, so every setting the service depends on is visible in one place.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every setting the service needs to run.
type Config struct {
	HTTPAddr string
	GRPCAddr string

	// AdminAddr serves metrics and pprof. It is a separate listener so it can
	// be bound to an internal interface; never expose it publicly.
	AdminAddr string

	JWTSecret       string
	JWTTTL          time.Duration
	ShutdownTimeout time.Duration
	LogLevel        slog.Level

	Mongo Mongo
	Redis Redis
	Kafka Kafka
}

// Mongo is required: the service has no useful behaviour without it.
type Mongo struct {
	URI      string
	Database string
}

// Redis is optional. It is configured ahead of the domains that will use it for
// caching and distributed locking, so until one does, an unset REDIS_ADDR simply
// leaves the feature off rather than blocking startup.
type Redis struct {
	Addr     string
	Password string
	DB       int

	// TTL bounds how long a cached entry may be stale. It is a ceiling, not the
	// mechanism: writes invalidate their own keys, and this only catches the
	// entries a missed invalidation would otherwise strand forever.
	TTL time.Duration
}

// Enabled reports whether a Redis endpoint was configured.
func (r Redis) Enabled() bool { return r.Addr != "" }

// Kafka is optional, on the same terms as Redis.
type Kafka struct {
	Brokers []string
	GroupID string
}

// Enabled reports whether any Kafka broker was configured.
func (k Kafka) Enabled() bool { return len(k.Brokers) > 0 }

// minSecretLen guards against an HS256 key short enough to brute force.
const minSecretLen = 32

// Load reads configuration from the environment.
//
// Every problem is collected and reported together, so a misconfigured
// deployment does not have to be fixed one variable per restart.
func Load() (Config, error) {
	var l loader

	cfg := Config{
		HTTPAddr:        l.optional("HTTP_ADDR", ":8080"),
		GRPCAddr:        l.optional("GRPC_ADDR", ":9090"),
		AdminAddr:       l.optional("ADMIN_ADDR", ":6060"),
		JWTSecret:       l.required("JWT_SECRET"),
		JWTTTL:          l.duration("JWT_TTL", 24*time.Hour),
		ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		LogLevel:        l.level("LOG_LEVEL", slog.LevelInfo),

		Mongo: Mongo{
			URI:      l.required("MONGO_URI"),
			Database: l.optional("MONGO_DATABASE", "ecommerce"),
		},
		Redis: Redis{
			Addr:     os.Getenv("REDIS_ADDR"),
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       l.integer("REDIS_DB", 0),
			TTL:      l.duration("REDIS_TTL", 5*time.Minute),
		},
		Kafka: Kafka{
			Brokers: l.list("KAFKA_BROKERS"),
			GroupID: os.Getenv("KAFKA_GROUP_ID"),
		},
	}

	if n := len(cfg.JWTSecret); n > 0 && n < minSecretLen {
		l.problems = append(l.problems,
			fmt.Sprintf("JWT_SECRET must be at least %d characters, got %d", minSecretLen, n))
	}

	// A consumer group is not defaulted on purpose: two services sharing an
	// accidental default group would silently steal each other's messages.
	if cfg.Kafka.Enabled() && cfg.Kafka.GroupID == "" {
		l.problems = append(l.problems, "KAFKA_GROUP_ID is required when KAFKA_BROKERS is set")
	}

	if len(l.problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration:\n  - %s",
			strings.Join(l.problems, "\n  - "))
	}
	return cfg, nil
}

// loader accumulates configuration problems so Load can report them all at once.
type loader struct {
	problems []string
}

func (l *loader) required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		l.problems = append(l.problems, key+" is required but not set")
	}
	return v
}

func (l *loader) optional(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// list reads a comma-separated value, dropping surrounding spaces and empties.
func (l *loader) list(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (l *loader) integer(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s is not a valid integer: %q", key, raw))
		return fallback
	}
	return n
}

func (l *loader) duration(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s is not a valid duration: %q", key, raw))
		return fallback
	}
	return d
}

func (l *loader) level(key string, fallback slog.Level) slog.Level {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(raw)); err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s is not a valid log level: %q", key, raw))
		return fallback
	}
	return lvl
}
