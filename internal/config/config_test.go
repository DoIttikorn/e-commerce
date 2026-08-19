package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DoIttikorn/e-commerce/internal/config"
)

// validEnv is the smallest environment Load accepts.
func validEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MONGO_URI", "mongodb://localhost:27017")
	t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
}

func TestLoadAppliesDefaults(t *testing.T) {
	validEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want \":8080\"", cfg.HTTPAddr)
	}
	if cfg.Mongo.Database != "ecommerce" {
		t.Errorf("Mongo.Database = %q, want \"ecommerce\"", cfg.Mongo.Database)
	}
	if cfg.JWTTTL != 24*time.Hour {
		t.Errorf("JWTTTL = %v, want 24h", cfg.JWTTTL)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
}

// Redis and Kafka are opt-in: an environment that mentions neither must still
// start, because no domain depends on them yet.
func TestOptionalInfrastructureDefaultsToDisabled(t *testing.T) {
	validEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Redis.Enabled() {
		t.Error("Redis.Enabled() = true with no REDIS_ADDR set")
	}
	if cfg.Kafka.Enabled() {
		t.Error("Kafka.Enabled() = true with no KAFKA_BROKERS set")
	}
}

func TestRedisConfig(t *testing.T) {
	validEnv(t)
	t.Setenv("REDIS_ADDR", "localhost:6379")
	t.Setenv("REDIS_DB", "3")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.Redis.Enabled() {
		t.Error("Redis.Enabled() = false, want true")
	}
	if cfg.Redis.DB != 3 {
		t.Errorf("Redis.DB = %d, want 3", cfg.Redis.DB)
	}
}

func TestKafkaBrokersAreSplitAndTrimmed(t *testing.T) {
	validEnv(t)
	t.Setenv("KAFKA_BROKERS", " a:9092 , b:9092 ,, ")
	t.Setenv("KAFKA_GROUP_ID", "ecommerce")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := []string{"a:9092", "b:9092"}
	if len(cfg.Kafka.Brokers) != len(want) {
		t.Fatalf("Brokers = %q, want %q", cfg.Kafka.Brokers, want)
	}
	for i := range want {
		if cfg.Kafka.Brokers[i] != want[i] {
			t.Fatalf("Brokers = %q, want %q", cfg.Kafka.Brokers, want)
		}
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantIn  string
		omitURI bool
	}{
		{
			name:    "missing required",
			omitURI: true,
			wantIn:  "MONGO_URI is required",
		},
		{
			name:   "short jwt secret",
			env:    map[string]string{"JWT_SECRET": "tooshort"},
			wantIn: "JWT_SECRET must be at least 32 characters",
		},
		{
			name:   "bad duration",
			env:    map[string]string{"JWT_TTL": "not-a-duration"},
			wantIn: "JWT_TTL is not a valid duration",
		},
		{
			name:   "bad redis db",
			env:    map[string]string{"REDIS_ADDR": "localhost:6379", "REDIS_DB": "abc"},
			wantIn: "REDIS_DB is not a valid integer",
		},
		{
			// Defaulting a consumer group would let two services silently
			// consume from each other's offsets.
			name:   "kafka brokers without group",
			env:    map[string]string{"KAFKA_BROKERS": "localhost:9092"},
			wantIn: "KAFKA_GROUP_ID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.omitURI {
				validEnv(t)
			} else {
				t.Setenv("JWT_SECRET", strings.Repeat("x", 32))
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			_, err := config.Load()
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantIn)
			}
		})
	}
}

// Load reports every problem at once so a broken environment takes one restart
// to diagnose rather than one per variable.
func TestLoadReportsAllProblemsTogether(t *testing.T) {
	t.Setenv("JWT_SECRET", "short")
	t.Setenv("LOG_LEVEL", "bogus")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want an error")
	}

	for _, want := range []string{"MONGO_URI", "JWT_SECRET", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}
