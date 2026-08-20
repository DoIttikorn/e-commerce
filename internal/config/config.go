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

	// GRPCTLSDir holds the mutual-TLS material for the internal gRPC link.
	//
	// Empty means plaintext, which is what the test suite and a single-machine
	// run use. Services say so loudly at startup rather than letting an
	// unauthenticated internal port pass for a configured one.
	GRPCTLSDir string

	// ProductGRPCAddr is where the Product service answers stock reservations.
	// Only the Order service uses it, and it fails fast at startup if a service
	// that needs it has none.
	ProductGRPCAddr string

	// JWTSecret is the HMAC secret. Used when no key pair is configured, which
	// is the single-service case the brief describes.
	JWTSecret string

	// JWTPrivateKey and JWTPublicKey are base64-encoded PEM, and configure the
	// asymmetric mode. Only the issuing service should have the private half:
	// with a shared HMAC secret every service can mint tokens, and across six
	// services that turns a bug anywhere into an impersonation everywhere.
	//
	// Base64 because a PEM block's newlines do not survive most of the ways an
	// environment variable gets set.
	JWTPrivateKey string
	JWTPublicKey  string

	JWTTTL          time.Duration
	ShutdownTimeout time.Duration
	LogLevel        slog.Level

	Mongo   Mongo
	Redis   Redis
	Kafka   Kafka
	Tracing Tracing
}

// Tracing is optional, on the same terms as Redis and Kafka.
//
// Unset OTEL_EXPORTER_OTLP_ENDPOINT and the service still propagates whatever
// trace context arrives with a request — it simply exports nothing of its own.
// That is deliberate: a service that drops the context it was handed punches a
// hole in a trace that other services are still filling in.
type Tracing struct {
	// Endpoint is the OTLP/gRPC collector, host:port. The variable keeps its
	// OpenTelemetry-standard name so an operator who knows the ecosystem does
	// not have to learn a private one.
	Endpoint string

	// SampleRatio is the fraction of root traces recorded.
	//
	// 1.0 is right for development and wrong for production: at real traffic
	// the collector, not the service, becomes the bottleneck, and the bill
	// follows the volume. The decision is taken once at the root and honoured
	// downstream, so a sampled trace is whole rather than partial.
	SampleRatio float64
}

// Enabled reports whether a trace collector was configured.
func (t Tracing) Enabled() bool { return t.Endpoint != "" }

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

	// Partitions is how many a topic gets when this service creates it.
	//
	// It is the ceiling on consumer parallelism: a group can have at most one
	// consumer per partition, so a topic with one partition can never be
	// consumed by more than one instance however many are running. Raising it
	// later is possible; lowering it is not, and repartitioning changes which
	// key lands where — so it is worth choosing rather than defaulting.
	Partitions int
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
		ProductGRPCAddr: os.Getenv("PRODUCT_GRPC_ADDR"),
		GRPCTLSDir:      os.Getenv("GRPC_TLS_DIR"),
		JWTSecret:       os.Getenv("JWT_SECRET"),
		JWTPrivateKey:   os.Getenv("JWT_PRIVATE_KEY"),
		JWTPublicKey:    os.Getenv("JWT_PUBLIC_KEY"),
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
			Brokers:    l.list("KAFKA_BROKERS"),
			GroupID:    os.Getenv("KAFKA_GROUP_ID"),
			Partitions: l.integer("KAFKA_PARTITIONS", 3),
		},
		Tracing: Tracing{
			Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
			SampleRatio: l.ratio("OTEL_TRACES_SAMPLER_ARG", 1.0),
		},
	}

	// One of the two schemes has to be configured. Neither is defaulted: a
	// service that starts with a guessed signing key is worse than one that
	// refuses to start.
	if cfg.JWTSecret == "" && cfg.JWTPublicKey == "" {
		l.problems = append(l.problems,
			"either JWT_SECRET or JWT_PUBLIC_KEY is required; run `make keys` to generate a key pair")
	}
	if n := len(cfg.JWTSecret); n > 0 && n < minSecretLen {
		l.problems = append(l.problems,
			fmt.Sprintf("JWT_SECRET must be at least %d characters, got %d", minSecretLen, n))
	}
	// Asymmetric mode is deliberately lopsided, and the check has to be too.
	// A public key with no private key is the *correct* configuration for
	// every service except the issuer: it can verify a token and could not
	// mint one however thoroughly it was compromised. That is the entire
	// reason for splitting the key.
	//
	// The reverse is not valid: NewIssuerFrom needs the public half as well,
	// and a service that can sign but not verify is nothing anybody wants.
	if cfg.JWTPrivateKey != "" && cfg.JWTPublicKey == "" {
		l.problems = append(l.problems,
			"JWT_PRIVATE_KEY requires JWT_PUBLIC_KEY; the issuer needs both halves")
	}

	// A consumer group is not defaulted on purpose: two services sharing an
	// accidental default group would silently steal each other's messages.
	if cfg.Kafka.Enabled() && cfg.Kafka.GroupID == "" {
		l.problems = append(l.problems, "KAFKA_GROUP_ID is required when KAFKA_BROKERS is set")
	}
	if cfg.Kafka.Partitions < 1 {
		l.problems = append(l.problems, "KAFKA_PARTITIONS must be at least 1")
	}

	// Out of range is a typo, not an intention. Silently clamping 50 to 1 would
	// hide somebody meaning 50% and getting everything.
	if r := cfg.Tracing.SampleRatio; r < 0 || r > 1 {
		l.problems = append(l.problems,
			fmt.Sprintf("OTEL_TRACES_SAMPLER_ARG must be between 0 and 1, got %v", r))
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

func (l *loader) ratio(key string, fallback float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		l.problems = append(l.problems, fmt.Sprintf("%s is not a valid number: %q", key, raw))
		return fallback
	}
	return f
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
