package integration

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// Each service has its own MongoDB instance, so a test has to say which one it
// means. The default ports match docker-compose.yml.
var mongoPorts = map[string]int{
	"user":        27017,
	"seller":      27018,
	"product":     27019,
	"order":       27020,
	"marketplace": 27021,
	"live":        27022,
}

// mongoFor connects to one service's own instance.
//
// directConnection is not optional from the host: each replica set advertises
// its member as "mongo-<service>:27017", a name that resolves inside the
// compose network and nowhere else. Without it the driver discovers the set,
// tries the advertised address, and times out.
func mongoFor(t *testing.T, service string) (*mongo.Database, context.Context) {
	t.Helper()

	if testing.Short() {
		t.Skip("needs MongoDB; run make itest")
	}

	port, ok := mongoPorts[service]
	if !ok {
		t.Fatalf("no MongoDB instance is defined for %q", service)
	}

	uri := os.Getenv("MONGO_URI_" + strings.ToUpper(service))
	if uri == "" {
		uri = fmt.Sprintf("mongodb://127.0.0.1:%d/?directConnection=true", port)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect %s mongo: %v", service, err)
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		t.Fatalf("%s mongo unreachable at %s: %v", service, uri, err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	return client.Database(envOr("MONGO_DATABASE", "ecommerce_test")), ctx
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// dropAll clears collections so a test starts from a known state, and
// re-creates indexes through the adapter rather than assuming they survived.
func dropAll(t *testing.T, ctx context.Context, db *mongo.Database, collections ...string) {
	t.Helper()
	for _, name := range collections {
		if err := db.Collection(name).Drop(ctx); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}
}
