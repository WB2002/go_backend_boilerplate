package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"go_backend_boilerplate/internal/config"
)

// Client is a type alias for *goredis.Client.
type Client = goredis.Client

// New creates and initializes a new Go-Redis client.
func New(cfg config.RedisConfig) *goredis.Client {
	return NewClient(cfg)
}

// NewRedis creates and initializes a new Go-Redis client.
func NewRedis(cfg config.RedisConfig) *goredis.Client {
	return NewClient(cfg)
}

// NewClient creates and initializes a new Go-Redis client with pooling and backoff.
func NewClient(cfg config.RedisConfig) *goredis.Client {
	port := cfg.Port
	if port <= 0 {
		port = 6379
	}

	host := cfg.Host
	if host == "" {
		host = "localhost"
	}

	poolSize := cfg.PoolSize
	if poolSize <= 0 {
		poolSize = 50
	}

	minIdleConns := cfg.MinIdleConns
	if minIdleConns < 0 {
		minIdleConns = 10
	}

	addr := fmt.Sprintf("%s:%d", host, port)

	opts := &goredis.Options{
		Addr:            addr,
		Password:        cfg.Password,
		DB:              cfg.DB,
		PoolSize:        poolSize,
		MinIdleConns:    minIdleConns,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
		MaxRetries:      3,
		MinRetryBackoff: 100 * time.Millisecond,
		MaxRetryBackoff: 500 * time.Millisecond,
	}

	return goredis.NewClient(opts)
}

// Ping performs a health check probe against Redis with context deadline.
func Ping(ctx context.Context, client *goredis.Client) error {
	if client == nil {
		return errors.New("nil redis client")
	}
	return client.Ping(ctx).Err()
}

// Close gracefully terminates all connections in the client's pool.
func Close(client *goredis.Client) error {
	if client == nil {
		return nil
	}
	return client.Close()
}
