package jobs

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/platform/logger"
)

// HandlerFunc defines the callback signature for processing a background job.
type HandlerFunc func(ctx context.Context, job *Job) error

// WorkerOption configures WorkerPool runtime properties.
type WorkerOption func(*WorkerPool)

// WithConcurrency sets the number of concurrent worker goroutines.
func WithConcurrency(n int) WorkerOption {
	return func(p *WorkerPool) {
		if n > 0 {
			p.concurrency = n
		}
	}
}

// WithLeaseDuration sets how long a claimed lease is held before expiring.
func WithLeaseDuration(d time.Duration) WorkerOption {
	return func(p *WorkerPool) {
		if d > 0 {
			p.leaseDuration = d
		}
	}
}

// WithSweepInterval sets the interval for the periodic orphan lease reclaimer.
func WithSweepInterval(d time.Duration) WorkerOption {
	return func(p *WorkerPool) {
		if d > 0 {
			p.sweepInterval = d
		}
	}
}

// WithPollInterval sets the sleep duration when no jobs are available in any queue.
func WithPollInterval(d time.Duration) WorkerOption {
	return func(p *WorkerPool) {
		if d > 0 {
			p.pollInterval = d
		}
	}
}

// WithWorkerIDPrefix sets the worker instance naming prefix.
func WithWorkerIDPrefix(prefix string) WorkerOption {
	return func(p *WorkerPool) {
		if prefix != "" {
			p.workerIDPrefix = prefix
		}
	}
}

// WithLogger sets the Zerolog logger for the worker pool.
func WithLogger(l *zerolog.Logger) WorkerOption {
	return func(p *WorkerPool) {
		if l != nil {
			p.logger = *l
		}
	}
}

// WorkerPool manages distributed background workers and periodic lease reclamation.
type WorkerPool struct {
	ledger         Ledger
	concurrency    int
	leaseDuration  time.Duration
	sweepInterval  time.Duration
	pollInterval   time.Duration
	workerIDPrefix string
	handlers       map[string]HandlerFunc
	handlersMu     sync.RWMutex
	logger         zerolog.Logger
}

// NewWorkerPool creates and configures a new background WorkerPool.
func NewWorkerPool(ledger Ledger, cfg config.JobsConfig, opts ...WorkerOption) *WorkerPool {
	concurrency := cfg.WorkerConcurrency
	if concurrency <= 0 {
		concurrency = 10
	}

	leaseDuration := time.Duration(cfg.LeaseDurationSec) * time.Second
	if leaseDuration <= 0 {
		leaseDuration = 30 * time.Second
	}

	sweepInterval := time.Duration(cfg.LeaseReclaimIntervalMs) * time.Millisecond
	if sweepInterval <= 0 {
		sweepInterval = 10 * time.Second
	}

	pool := &WorkerPool{
		ledger:         ledger,
		concurrency:    concurrency,
		leaseDuration:  leaseDuration,
		sweepInterval:  sweepInterval,
		pollInterval:   100 * time.Millisecond,
		workerIDPrefix: "worker-" + uuid.New().String()[:8],
		handlers:       make(map[string]HandlerFunc),
		logger:         *logger.Default(),
	}

	for _, opt := range opts {
		opt(pool)
	}

	return pool
}

// RegisterHandler binds a queue name to its processing handler function.
func (p *WorkerPool) RegisterHandler(queue string, handler HandlerFunc) {
	p.handlersMu.Lock()
	defer p.handlersMu.Unlock()
	p.handlers[queue] = handler
}

// ReclaimOrphanLeases triggers a sweep across registered queues to recover stalled claimed jobs.
func (p *WorkerPool) ReclaimOrphanLeases(ctx context.Context) (int64, error) {
	p.handlersMu.RLock()
	queues := make([]string, 0, len(p.handlers))
	for q := range p.handlers {
		queues = append(queues, q)
	}
	p.handlersMu.RUnlock()

	if len(queues) == 0 {
		return p.ledger.ReclaimOrphanLeases(ctx, "")
	}

	var totalReclaimed int64
	for _, q := range queues {
		count, err := p.ledger.ReclaimOrphanLeases(ctx, q)
		if err != nil {
			p.logger.Error().Err(err).Str("queue", q).Msg("failed to reclaim orphan leases")
			return totalReclaimed, err
		}
		totalReclaimed += count
	}

	return totalReclaimed, nil
}

// Start launches worker goroutines and the periodic sweeper, blocking until ctx is cancelled.
func (p *WorkerPool) Start(ctx context.Context) error {
	p.logger.Info().
		Int("concurrency", p.concurrency).
		Dur("lease_duration", p.leaseDuration).
		Dur("sweep_interval", p.sweepInterval).
		Msg("starting worker pool")

	var wg sync.WaitGroup

	// Periodic orphan lease reclaimer sweeper
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(p.sweepInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepCtx, cancel := context.WithTimeout(context.Background(), p.leaseDuration)
				reclaimed, err := p.ReclaimOrphanLeases(sweepCtx)
				cancel()
				if err != nil {
					p.logger.Warn().Err(err).Msg("orphan lease sweeper encountered error")
				} else if reclaimed > 0 {
					p.logger.Info().Int64("reclaimed", reclaimed).Msg("orphan leases recovered to pending")
				}
			}
		}
	}()

	// Spawn concurrency workers
	for i := 0; i < p.concurrency; i++ {
		workerIdx := i + 1
		workerID := fmt.Sprintf("%s-%d", p.workerIDPrefix, workerIdx)

		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			p.runWorker(ctx, id)
		}(workerID)
	}

	<-ctx.Done()
	p.logger.Info().Msg("shutdown requested, waiting for active jobs to complete...")
	wg.Wait()
	p.logger.Info().Msg("worker pool stopped cleanly")

	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return ctx.Err()
}

func (p *WorkerPool) runWorker(ctx context.Context, workerID string) {
	currentIdleBackoff := p.pollInterval

	for {
		if ctx.Err() != nil {
			return
		}

		p.handlersMu.RLock()
		queues := make([]string, 0, len(p.handlers))
		for q := range p.handlers {
			queues = append(queues, q)
		}
		p.handlersMu.RUnlock()

		if len(queues) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(currentIdleBackoff):
				if currentIdleBackoff < 1*time.Second {
					currentIdleBackoff = currentIdleBackoff * 2
					if currentIdleBackoff > 1*time.Second {
						currentIdleBackoff = 1 * time.Second
					}
				}
				continue
			}
		}

		claimedAny := false
		for _, queue := range queues {
			if ctx.Err() != nil {
				return
			}

			p.handlersMu.RLock()
			handler, hasHandler := p.handlers[queue]
			p.handlersMu.RUnlock()

			if !hasHandler {
				continue
			}

			job, err := p.ledger.ClaimNext(ctx, queue, workerID, p.leaseDuration)
			if err != nil {
				p.logger.Error().Err(err).Str("queue", queue).Str("worker", workerID).Msg("error claiming next job")
				continue
			}

			if job != nil {
				claimedAny = true
				p.executeJobWithPanicShield(ctx, handler, job)
			}
		}

		if claimedAny {
			currentIdleBackoff = p.pollInterval
		} else {
			select {
			case <-ctx.Done():
				return
			case <-time.After(currentIdleBackoff):
			}
			if currentIdleBackoff < 1*time.Second {
				currentIdleBackoff = currentIdleBackoff * 2
				if currentIdleBackoff > 1*time.Second {
					currentIdleBackoff = 1 * time.Second
				}
			}
		}
	}
}

func (p *WorkerPool) executeJobWithPanicShield(ctx context.Context, handler HandlerFunc, job *Job) {
	defer func() {
		if r := recover(); r != nil {
			stack := string(debug.Stack())
			panicErr := fmt.Errorf("job handler panic: %v\nstack:\n%s", r, stack)
			p.logger.Error().
				Str("job_id", job.ID).
				Str("queue", job.Queue).
				Interface("panic", r).
				Str("stack", stack).
				Msg("shielded panic in job execution")

			failCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			retryDelay := time.Duration(job.Attempts+1) * 200 * time.Millisecond
			_ = p.ledger.Fail(failCtx, job.ID, panicErr, retryDelay)
		}
	}()

	jobCtx, cancel := context.WithTimeout(ctx, p.leaseDuration)
	defer cancel()

	if err := handler(jobCtx, job); err != nil {
		// If job failed or was canceled due to worker pool shutdown, release back to pending without penalty
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(jobCtx.Err(), context.Canceled)) {
			p.logger.Info().
				Str("job_id", job.ID).
				Str("queue", job.Queue).
				Msg("worker pool shutting down; releasing in-flight job back to pending without penalty")
			relCtx, relCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer relCancel()
			_ = p.ledger.Release(relCtx, job.ID)
			return
		}

		p.logger.Error().
			Err(err).
			Str("job_id", job.ID).
			Str("queue", job.Queue).
			Msg("job execution returned error")

		failCtx, failCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer failCancel()
		retryDelay := time.Duration(job.Attempts+1) * 200 * time.Millisecond
		_ = p.ledger.Fail(failCtx, job.ID, err, retryDelay)
	} else {
		completeCtx, completeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer completeCancel()
		_ = p.ledger.Complete(completeCtx, job.ID)
	}
}
