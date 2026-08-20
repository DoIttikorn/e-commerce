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
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

// NewMongo connects to MongoDB and verifies the deployment is reachable.
//
// mongo.Connect validates options but never contacts the server, so without
// the ping an unreachable database would surface as a failed request long
// after startup instead of as a failed startup.
// The concerns are set explicitly rather than left to the driver, because they
// are the settings that decide whether a single-node development set and a
// multi-node production set behave the same way.
//
//   - Write concern majority: a write is acknowledged once a majority of
//     members have it, so a failover cannot roll it back. On the single-node
//     sets compose runs this is identical to w:1 and costs nothing — which is
//     the point. The code is already correct when the deployment grows, rather
//     than needing a change nobody remembers to make.
//   - Read concern majority: reads see only data that cannot be rolled back.
//     Without it a read can return a write that a subsequent election erases,
//     which is how "the order was created and then it wasn't" happens.
//   - Read preference primary: no stale reads. Secondaries lag, and in a
//     system where a reservation is decremented and then read back, lag is
//     wrong answers rather than old ones.
//
// Journalling is implied by majority on any modern deployment, so setting it
// separately would only be a way to get it wrong.
func NewMongo(ctx context.Context, uri, database string) (*mongo.Database, error) {
	client, err := mongo.Connect(options.Client().
		ApplyURI(uri).
		SetWriteConcern(writeconcern.Majority()).
		SetReadConcern(readconcern.Majority()).
		SetReadPreference(readpref.Primary()))
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
