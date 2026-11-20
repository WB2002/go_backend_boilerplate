package mongodb_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/platform/mongodb"
)

func TestMongoConnectionConfig(t *testing.T) {
	cfg := config.MongoConfig{
		URI:         "mongodb://invalid-host:27017/?connectTimeoutMS=100",
		TimeoutMs:   100,
		MaxPoolSize: 30,
		MinPoolSize: 5,
	}

	// Non-blocking lazy connect: should succeed without error even if host is unreachable
	client, err := mongodb.New(cfg)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer func() {
		_ = mongodb.Close(context.Background(), client)
	}()

	// Health check ping should fail on invalid host
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err = mongodb.Ping(ctx, client)
	assert.Error(t, err)
}

func TestMongoMissingURI(t *testing.T) {
	cfg := config.MongoConfig{
		URI: "",
	}
	_, err := mongodb.New(cfg)
	assert.Error(t, err)
}

func TestMongoNilHelpers(t *testing.T) {
	ctx := context.Background()
	err := mongodb.Ping(ctx, nil)
	assert.Error(t, err)

	err = mongodb.Close(ctx, nil)
	assert.NoError(t, err)
}
