package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/jobs"
	"go_backend_boilerplate/internal/core/outbox"
	"go_backend_boilerplate/internal/platform/database"
	"go_backend_boilerplate/internal/platform/logger"
	"go_backend_boilerplate/internal/platform/redis"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	appLogger := logger.New(cfg.App.Env)

	appLogger.Info().
		Str("app", cfg.App.Name).
		Str("env", cfg.App.Env).
		Int("concurrency", cfg.Jobs.WorkerConcurrency).
		Int("lease_duration_sec", cfg.Jobs.LeaseDurationSec).
		Int("lease_reclaim_interval_ms", cfg.Jobs.LeaseReclaimIntervalMs).
		Msg("starting background jobs worker daemon")

	db, err := database.New(cfg.Database)
	if err != nil {
		appLogger.Fatal().Err(err).Msg("failed to connect to PostgreSQL database")
	}
	defer func() {
		_ = database.Close(db)
	}()

	rdb := redis.New(cfg.Redis)
	if pingErr := redis.Ping(context.Background(), rdb); pingErr != nil {
		appLogger.Warn().Err(pingErr).Msg("redis connection unavailable, running in postgres-only ledger mode")
	} else {
		defer func() {
			_ = redis.Close(rdb)
		}()
	}

	ledger := jobs.New(db, rdb)
	pool := jobs.NewWorkerPool(ledger, cfg.Jobs, jobs.WithLogger(appLogger))

	// Register sample/default queues
	pool.RegisterHandler("default", func(ctx context.Context, job *jobs.Job) error {
		appLogger.Info().
			Str("job_id", job.ID).
			Str("queue", job.Queue).
			Msg("processing default background job")
		return nil
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Launch autonomous transactional outbox relay daemon
	outboxPub := outbox.NewRedisPublisher(rdb)
	outboxRelay := outbox.NewRelay(db, outboxPub, outbox.RelayConfig{
		BatchSize:    50,
		PollInterval: 500 * time.Millisecond,
		MaxRetries:   5,
	})
	go func() {
		if err := outboxRelay.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			appLogger.Error().Err(err).Msg("outbox relay daemon stopped with error")
		}
	}()

	if err := pool.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		appLogger.Fatal().Err(err).Msg("worker pool terminated with error")
	}

	appLogger.Info().Msg("background worker daemon stopped gracefully")
}
