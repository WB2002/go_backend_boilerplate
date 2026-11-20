package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/jobs"
)

func TestJobDeduplicationAndLeaseReclamation(t *testing.T) {
	// Mock or SQLite test for ledger logic
	ledger := jobs.NewTestLedger(t)

	ctx := context.Background()
	jobID, err := ledger.Enqueue(ctx, "invoices", []byte(`{"orderId":"123"}`), "inv-123")
	require.NoError(t, err)
	assert.NotEmpty(t, jobID)

	// Duplicate enqueue with same deduplicationKey returns existing or error
	_, err = ledger.Enqueue(ctx, "invoices", []byte(`{"orderId":"123"}`), "inv-123")
	assert.ErrorContains(t, err, "duplicate")

	// Claim job with 1 second lease
	claimed, err := ledger.ClaimNext(ctx, "invoices", "worker-1", 1*time.Second)
	require.NoError(t, err)
	assert.Equal(t, jobID, claimed.ID)

	// Simulate worker crash: lease expires after 1s
	time.Sleep(1100 * time.Millisecond)

	// Reclaimer sweeps and marks stalled job back to pending
	reclaimedCount, err := ledger.ReclaimOrphanLeases(ctx, "invoices")
	require.NoError(t, err)
	assert.Equal(t, int64(1), reclaimedCount)
}

func TestConcurrentClaimsAtomic(t *testing.T) {
	ledger := jobs.NewTestLedger(t)
	ctx := context.Background()

	numJobs := 30
	for i := 0; i < numJobs; i++ {
		key := fmt.Sprintf("dedup-%d", i)
		_, err := ledger.Enqueue(ctx, "batch-queue", []byte(fmt.Sprintf(`{"item":%d}`, i)), key)
		require.NoError(t, err)
	}

	numWorkers := 10
	var claimedMap sync.Map
	var totalClaimed int64
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		workerID := fmt.Sprintf("worker-%d", w)
		go func(wID string) {
			defer wg.Done()
			for {
				job, err := ledger.ClaimNext(ctx, "batch-queue", wID, 5*time.Second)
				if err != nil || job == nil {
					return
				}

				// Verify atomic claim: no job claimed twice
				_, loaded := claimedMap.LoadOrStore(job.ID, wID)
				assert.False(t, loaded, "job %s was claimed more than once!", job.ID)
				atomic.AddInt64(&totalClaimed, 1)

				// Mark complete
				err = ledger.Complete(ctx, job.ID)
				assert.NoError(t, err)
			}
		}(workerID)
	}

	wg.Wait()
	assert.Equal(t, int64(numJobs), totalClaimed)
}

func TestWorkerPoolExecution(t *testing.T) {
	ledger := jobs.NewTestLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := config.JobsConfig{
		WorkerConcurrency:      4,
		LeaseDurationSec:       5,
		LeaseReclaimIntervalMs: 200,
	}

	pool := jobs.NewWorkerPool(ledger, cfg,
		jobs.WithPollInterval(20*time.Millisecond),
	)

	var processedCount int32
	pool.RegisterHandler("emails", func(ctx context.Context, job *jobs.Job) error {
		atomic.AddInt32(&processedCount, 1)
		return nil
	})

	for i := 0; i < 5; i++ {
		_, err := ledger.Enqueue(ctx, "emails", []byte(`{"to":"user@example.com"}`), fmt.Sprintf("email-%d", i))
		require.NoError(t, err)
	}

	poolErrCh := make(chan error, 1)
	go func() {
		poolErrCh <- pool.Start(ctx)
	}()

	// Wait for processing
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&processedCount) == 5
	}, 3*time.Second, 50*time.Millisecond)

	cancel()
	err := <-poolErrCh
	assert.NoError(t, err)
}

func TestWorkerPoolPanicRecovery(t *testing.T) {
	ledger := jobs.NewTestLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := config.JobsConfig{
		WorkerConcurrency:      2,
		LeaseDurationSec:       5,
		LeaseReclaimIntervalMs: 500,
	}

	pool := jobs.NewWorkerPool(ledger, cfg,
		jobs.WithPollInterval(20*time.Millisecond),
	)

	var panicsTriggered int32
	pool.RegisterHandler("dangerous", func(ctx context.Context, job *jobs.Job) error {
		atomic.AddInt32(&panicsTriggered, 1)
		panic("deliberate nil pointer exception inside job handler")
	})

	jobID, err := ledger.Enqueue(ctx, "dangerous", []byte(`{"danger":true}`), "danger-1")
	require.NoError(t, err)

	poolErrCh := make(chan error, 1)
	go func() {
		poolErrCh <- pool.Start(ctx)
	}()

	// Verify panic was triggered and handled without crashing the worker
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&panicsTriggered) >= 1
	}, 2*time.Second, 50*time.Millisecond)

	// Check job status in ledger: should have attempts incremented and last_error recorded
	require.Eventually(t, func() bool {
		j, err := ledger.GetJob(context.Background(), jobID)
		if err != nil {
			return false
		}
		return j.Attempts >= 1 && j.LastError != nil && assert.Contains(t, *j.LastError, "deliberate nil pointer")
	}, 2*time.Second, 50*time.Millisecond)

	cancel()
	err = <-poolErrCh
	assert.NoError(t, err)
}

func TestMaxRetriesFailure(t *testing.T) {
	ledger := jobs.NewTestLedger(t)
	ctx := context.Background()

	jobID, err := ledger.Enqueue(ctx, "failing", []byte(`{"fail":true}`), "fail-1")
	require.NoError(t, err)

	// Max retries is 3. Fail 3 times.
	for attempt := 1; attempt <= 3; attempt++ {
		claimed, err := ledger.ClaimNext(ctx, "failing", "worker-1", 1*time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claimed)
		assert.Equal(t, jobID, claimed.ID)

		err = ledger.Fail(ctx, claimed.ID, errors.New("simulated network failure"), 0)
		require.NoError(t, err)
	}

	// Should now be failed
	job, err := ledger.GetJob(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StateFailed, job.State)
	assert.Equal(t, 3, job.Attempts)

	// Next claim should yield nothing
	claimed, err := ledger.ClaimNext(ctx, "failing", "worker-1", 1*time.Minute)
	require.NoError(t, err)
	assert.Nil(t, claimed)
}

func TestPeriodicSweeper(t *testing.T) {
	ledger := jobs.NewTestLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := config.JobsConfig{
		WorkerConcurrency:      1,
		LeaseDurationSec:       1,
		LeaseReclaimIntervalMs: 100,
	}

	pool := jobs.NewWorkerPool(ledger, cfg,
		jobs.WithSweepInterval(100*time.Millisecond),
		jobs.WithPollInterval(50*time.Millisecond),
	)

	// Register dummy handler
	pool.RegisterHandler("tasks", func(ctx context.Context, job *jobs.Job) error {
		return nil
	})

	// Enqueue and manually claim to simulate orphaned worker
	jobID, err := ledger.Enqueue(ctx, "tasks", []byte(`{"task":1}`), "task-1")
	require.NoError(t, err)

	claimed, err := ledger.ClaimNext(ctx, "tasks", "dead-worker", 200*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	// Start pool: sweeper should recover the job after 200ms lease expires
	go func() {
		_ = pool.Start(ctx)
	}()

	// Wait for sweeper to reclaim and handler to process it to completion
	require.Eventually(t, func() bool {
		j, err := ledger.GetJob(context.Background(), jobID)
		if err != nil {
			return false
		}
		return j.State == jobs.StateCompleted
	}, 3*time.Second, 100*time.Millisecond)
}

func TestLedgerValidationAndErrors(t *testing.T) {
	ledger := jobs.NewTestLedger(t)
	ctx := context.Background()

	// Empty queue validation
	_, err := ledger.Enqueue(ctx, "", []byte("payload"), "")
	assert.ErrorContains(t, err, "empty")

	// Job not found
	err = ledger.Complete(ctx, "non-existent")
	assert.ErrorIs(t, err, jobs.ErrJobNotFound)

	err = ledger.Fail(ctx, "non-existent", errors.New("err"), 0)
	assert.ErrorIs(t, err, jobs.ErrJobNotFound)

	_, err = ledger.GetJob(ctx, "non-existent")
	assert.ErrorIs(t, err, jobs.ErrJobNotFound)
}

func TestPostgresLedgerWithSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:memdb_jobs?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err)

	err = db.AutoMigrate(&jobs.Job{})
	require.NoError(t, err)

	ledger := jobs.NewPostgresLedger(db, nil)
	ctx := context.Background()

	// Enqueue
	jobID, err := ledger.Enqueue(ctx, "billing", []byte(`{"invoice_id":"inv-999"}`), "dedup-999")
	require.NoError(t, err)
	assert.NotEmpty(t, jobID)

	// Deduplication check: duplicate active key returns error
	_, err = ledger.Enqueue(ctx, "billing", []byte(`{"invoice_id":"inv-999"}`), "dedup-999")
	assert.ErrorContains(t, err, "duplicate")

	// ClaimNext executes with clause.Locking
	claimed, err := ledger.ClaimNext(ctx, "billing", "worker-sql", 1*time.Second)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, jobID, claimed.ID)
	assert.Equal(t, jobs.StateClaimed, claimed.State)
	assert.Equal(t, "worker-sql", *claimed.ClaimedBy)

	// Release (without penalty)
	err = ledger.Release(ctx, jobID)
	require.NoError(t, err)
	released, err := ledger.GetJob(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StatePending, released.State)
	assert.Equal(t, 0, released.Attempts)

	// Claim again with short lease
	claimedAgain, err := ledger.ClaimNext(ctx, "billing", "worker-sql-2", 100*time.Millisecond)
	require.NoError(t, err)
	require.NotNil(t, claimedAgain)

	// Wait for lease expiration
	time.Sleep(150 * time.Millisecond)

	// ReclaimOrphanLeases
	reclaimedCount, err := ledger.ReclaimOrphanLeases(ctx, "billing")
	require.NoError(t, err)
	assert.Equal(t, int64(1), reclaimedCount)

	reclaimedJob, err := ledger.GetJob(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StatePending, reclaimedJob.State)
	assert.Equal(t, 1, reclaimedJob.Attempts)

	// Fail with backoff
	err = ledger.Fail(ctx, jobID, errors.New("transient error"), 200*time.Millisecond)
	require.NoError(t, err)

	failedJob, err := ledger.GetJob(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StatePending, failedJob.State)
	assert.Equal(t, 2, failedJob.Attempts)
	require.NotNil(t, failedJob.LeaseExpiresAt)

	// Complete
	err = ledger.Complete(ctx, jobID)
	require.NoError(t, err)

	completedJob, err := ledger.GetJob(ctx, jobID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StateCompleted, completedJob.State)
}

func TestWorkerPoolGracefulShutdownReleasesInFlightJob(t *testing.T) {
	ledger := jobs.NewTestLedger(t)
	ctx, cancel := context.WithCancel(context.Background())

	cfg := config.JobsConfig{
		WorkerConcurrency:      1,
		LeaseDurationSec:       5,
		LeaseReclaimIntervalMs: 1000,
	}

	pool := jobs.NewWorkerPool(ledger, cfg,
		jobs.WithPollInterval(20*time.Millisecond),
	)

	jobStarted := make(chan struct{})
	pool.RegisterHandler("long-jobs", func(jobCtx context.Context, job *jobs.Job) error {
		close(jobStarted)
		<-jobCtx.Done()
		return jobCtx.Err()
	})

	jobID, err := ledger.Enqueue(context.Background(), "long-jobs", []byte(`{"slow":true}`), "slow-1")
	require.NoError(t, err)

	poolDone := make(chan error, 1)
	go func() {
		poolDone <- pool.Start(ctx)
	}()

	<-jobStarted
	// Cancel pool context to simulate graceful SIGTERM
	cancel()

	err = <-poolDone
	assert.NoError(t, err)

	// Verify job was released back to pending with 0 attempts penalty
	job, err := ledger.GetJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.Equal(t, jobs.StatePending, job.State)
	assert.Equal(t, 0, job.Attempts)
	assert.Nil(t, job.ClaimedBy)
}

func TestDeduplicationScopeAndReuseAfterCompletion(t *testing.T) {
	ledger := jobs.NewTestLedger(t)
	ctx := context.Background()

	// Enqueue in queue-A
	jobA, err := ledger.Enqueue(ctx, "queue-A", []byte("data"), "key-1")
	require.NoError(t, err)

	// Same key in queue-B should succeed because dedup is scoped per queue
	jobB, err := ledger.Enqueue(ctx, "queue-B", []byte("data"), "key-1")
	require.NoError(t, err)
	assert.NotEqual(t, jobA, jobB)

	// Complete job in queue-A
	claimedA, err := ledger.ClaimNext(ctx, "queue-A", "worker-1", 1*time.Minute)
	require.NoError(t, err)
	err = ledger.Complete(ctx, claimedA.ID)
	require.NoError(t, err)

	// Re-enqueue in queue-A with same key-1: succeeds because previous job is completed
	jobA2, err := ledger.Enqueue(ctx, "queue-A", []byte("data"), "key-1")
	require.NoError(t, err)
	assert.NotEqual(t, jobA, jobA2)
}

func TestEnqueueTxRollback(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	err = db.AutoMigrate(&jobs.Job{})
	require.NoError(t, err)

	ledger := jobs.NewPostgresLedger(db, nil)
	ctx := context.Background()

	// 1. Transaction rolled back -> job must not be inserted
	_ = db.Transaction(func(tx *gorm.DB) error {
		_, err := ledger.EnqueueTx(ctx, tx, "email-queue", []byte("send-email"), "dedup-1")
		require.NoError(t, err)
		return errors.New("rollback transaction")
	})

	var count int64
	db.Model(&jobs.Job{}).Count(&count)
	assert.Equal(t, int64(0), count, "Job in rolled back transaction must not persist")

	// 2. Transaction committed -> job persists
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := ledger.EnqueueTx(ctx, tx, "email-queue", []byte("send-email"), "dedup-2")
		return err
	})
	assert.NoError(t, err)

	db.Model(&jobs.Job{}).Count(&count)
	assert.Equal(t, int64(1), count, "Job in committed transaction must persist")
}
