// Package database holds shared driver initialisation.
//
// It owns connection setup and health checking only. Queries and index
// definitions belong to each domain's repository adapter, so that adding a
// domain does not mean editing this package.
package database

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// NewMongo connects to MongoDB and verifies the deployment is reachable.
//
// mongo.Connect validates options but never contacts the server, so without
// the ping an unreachable database would surface as a failed request long
// after startup instead of as a failed startup.
func NewMongo(ctx context.Context, uri, database string) (*mongo.Database, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connect mongo: %w", err)
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		// Detach from ctx: if the ping failed because ctx expired, a
		// cancelled context would also abandon the cleanup.
		_ = client.Disconnect(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("ping mongo: %w", err)
	}

	return client.Database(database), nil
}
