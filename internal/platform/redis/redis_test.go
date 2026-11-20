package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/platform/redis"
)

func TestRedisClientConfig(t *testing.T) {
	cfg := config.RedisConfig{
		Host:         "invalid-host",
		Port:         6379,
		Password:     "secret",
		DB:           1,
		PoolSize:     25,
		MinIdleConns: 5,
	}

	client := redis.New(cfg)
	require.NotNil(t, client)
	defer redis.Close(client)

	opts := client.Options()
	assert.Equal(t, "invalid-host:6379", opts.Addr)
	assert.Equal(t, "secret", opts.Password)
	assert.Equal(t, 1, opts.DB)
	assert.Equal(t, 25, opts.PoolSize)
	assert.Equal(t, 5, opts.MinIdleConns)

	// Verify health check probe fails on unreachable host with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := redis.Ping(ctx, client)
	assert.Error(t, err)
}

func TestRedisNilHelpers(t *testing.T) {
	ctx := context.Background()
	err := redis.Ping(ctx, nil)
	assert.Error(t, err)

	err = redis.Close(nil)
	assert.NoError(t, err)
}
