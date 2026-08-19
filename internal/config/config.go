// Package config loads process configuration from the environment.
//
// Load is called once from main and nothing else in the codebase reads
// os.Getenv, so every setting the service depends on is visible in one place.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config holds every setting the service needs to run.
type Config struct {
	HTTPAddr        string
	GRPCAddr        string
	MongoURI        string
	MongoDatabase   string
	JWTSecret       string
	JWTTTL          time.Duration
	ShutdownTimeout time.Duration
	LogLevel        slog.Level
}

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
		MongoURI:        l.required("MONGO_URI"),
		MongoDatabase:   l.optional("MONGO_DATABASE", "ecommerce"),
		JWTSecret:       l.required("JWT_SECRET"),
		JWTTTL:          l.duration("JWT_TTL", 24*time.Hour),
		ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", 15*time.Second),
		LogLevel:        l.level("LOG_LEVEL", slog.LevelInfo),
	}

	if n := len(cfg.JWTSecret); n > 0 && n < minSecretLen {
		l.problems = append(l.problems,
			fmt.Sprintf("JWT_SECRET must be at least %d characters, got %d", minSecretLen, n))
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
