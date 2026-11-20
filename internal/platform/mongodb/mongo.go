package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"go_backend_boilerplate/internal/config"
)

// Client is a type alias for *mongo.Client.
type Client = mongo.Client

// New creates and configures a new MongoDB client.
func New(cfg config.MongoConfig) (*mongo.Client, error) {
	return NewMongo(cfg)
}

// NewClient creates and configures a new MongoDB client.
func NewClient(cfg config.MongoConfig) (*mongo.Client, error) {
	return NewMongo(cfg)
}

// Connect initializes and returns a MongoDB client (alias for New).
func Connect(cfg config.MongoConfig) (*mongo.Client, error) {
	return NewMongo(cfg)
}

// NewMongo initializes a MongoDB client using non-blocking lazy connect.
// The application can successfully start even if MongoDB is temporarily unavailable;
// health probes will reflect status via Ping.
func NewMongo(cfg config.MongoConfig) (*mongo.Client, error) {
	if cfg.URI == "" {
		return nil, errors.New("mongo URI is required")
	}

	timeout := time.Duration(cfg.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	maxPoolSize := cfg.MaxPoolSize
	if maxPoolSize == 0 {
		maxPoolSize = 50
	}
	minPoolSize := cfg.MinPoolSize

	clientOpts := options.Client().
		ApplyURI(cfg.URI).
		SetMaxPoolSize(maxPoolSize).
		SetMinPoolSize(minPoolSize).
		SetConnectTimeout(timeout).
		SetServerSelectionTimeout(timeout)

	client, err := mongo.Connect(context.Background(), clientOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize mongodb client: %w", err)
	}

	return client, nil
}

// Ping performs a health check ping against the primary MongoDB node.
func Ping(ctx context.Context, client *mongo.Client) error {
	if client == nil {
		return errors.New("nil mongo client")
	}
	return client.Ping(ctx, readpref.Primary())
}

// Close disconnects the MongoDB client and drains connection pools.
func Close(ctx context.Context, client *mongo.Client) error {
	if client == nil {
		return nil
	}
	return client.Disconnect(ctx)
}
