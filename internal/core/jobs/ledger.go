package jobs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Error definitions for job ledger operations.
var (
	// ErrDuplicateJob indicates a job with the specified deduplication key is already active.
	ErrDuplicateJob = errors.New("duplicate job with deduplication key")
	// ErrJobNotFound indicates no job record matched the requested identifier.
	ErrJobNotFound = errors.New("job not found")
	// ErrQueueEmpty indicates that no pending job was available to claim.
	ErrQueueEmpty = errors.New("queue is empty")
)

// JobState represents the lifecycle status of a background job in the ledger.
type JobState string

// Supported lifecycle states for jobs in the ledger.
const (
	StatePending   JobState = "pending"
	StateClaimed   JobState = "claimed"
	StateCompleted JobState = "completed"
	StateFailed    JobState = "failed"
)

// Job represents the persistent ledger model for background tasks.
type Job struct {
	ID               string     `gorm:"primaryKey;type:varchar(36)" json:"id"`
	Queue            string     `gorm:"type:varchar(255);not null;index:idx_background_jobs_queue_state_created_at,priority:1" json:"queue"`
	Payload          []byte     `gorm:"type:bytea;not null" json:"payload"`
	State            JobState   `gorm:"type:varchar(32);not null;default:'pending';index:idx_background_jobs_queue_state_created_at,priority:2;index:idx_background_jobs_state_lease_expires_at,priority:1" json:"state"`
	DeduplicationKey *string    `gorm:"type:varchar(255);index" json:"deduplication_key,omitempty"`
	ClaimedBy        *string    `gorm:"type:varchar(255)" json:"claimed_by,omitempty"`
	LeaseExpiresAt   *time.Time `gorm:"index:idx_background_jobs_state_lease_expires_at,priority:2" json:"lease_expires_at,omitempty"`
	Attempts         int        `gorm:"default:0;not null" json:"attempts"`
	MaxRetries       int        `gorm:"default:3;not null" json:"max_retries"`
	LastError        *string    `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt        time.Time  `gorm:"not null;index:idx_background_jobs_queue_state_created_at,priority:3" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"not null" json:"updated_at"`
}

// TableName specifies the PostgreSQL table name for GORM.
func (Job) TableName() string {
	return "background_jobs"
}

// JobEnqueueService defines the interface for enqueuing background tasks.
type JobEnqueueService interface {
	Enqueue(ctx context.Context, queue string, payload []byte, deduplicationKey string) (string, error)
}

// Ledger defines the ACID ledger operations for jobs persistence and lease coordination.
type Ledger interface {
	JobEnqueueService
	EnqueueTx(ctx context.Context, tx *gorm.DB, queue string, payload []byte, deduplicationKey string) (string, error)
	ClaimNext(ctx context.Context, queue string, workerID string, leaseDuration time.Duration) (*Job, error)
	Complete(ctx context.Context, jobID string) error
	Release(ctx context.Context, jobID string) error
	Fail(ctx context.Context, jobID string, jobErr error, retryDelay time.Duration) error
	ReclaimOrphanLeases(ctx context.Context, queue string) (int64, error)
	GetJob(ctx context.Context, jobID string) (*Job, error)
}

// PostgresLedger implements Ledger backed by PostgreSQL and optional Redis queue.
type PostgresLedger struct {
	db  *gorm.DB
	rdb *goredis.Client
}

// NewPostgresLedger creates a PostgreSQL-backed job ledger.
func NewPostgresLedger(db *gorm.DB, rdb *goredis.Client) *PostgresLedger {
	return &PostgresLedger{
		db:  db,
		rdb: rdb,
	}
}

// New creates a production Ledger backed by PostgreSQL and optional Redis.
func New(db *gorm.DB, rdb *goredis.Client) Ledger {
	return NewPostgresLedger(db, rdb)
}

// Enqueue inserts a new job into PostgreSQL with deduplication check and pushes to Redis queue if configured.
func (l *PostgresLedger) Enqueue(ctx context.Context, queue string, payload []byte, deduplicationKey string) (string, error) {
	return l.EnqueueTx(ctx, l.db, queue, payload, deduplicationKey)
}

// EnqueueTx inserts a new job into PostgreSQL inside an active database transaction.
func (l *PostgresLedger) EnqueueTx(ctx context.Context, tx *gorm.DB, queue string, payload []byte, deduplicationKey string) (string, error) {
	if queue == "" {
		return "", errors.New("queue cannot be empty")
	}

	targetDB := tx
	if targetDB == nil {
		targetDB = l.db
	}
	if targetDB == nil {
		return "", errors.New("database handle is nil")
	}

	jobID := uuid.New().String()
	now := time.Now()

	var dedup *string
	if deduplicationKey != "" {
		dedup = &deduplicationKey

		var existing Job
		err := targetDB.WithContext(ctx).
			Where("queue = ? AND deduplication_key = ? AND state IN (?, ?)", queue, deduplicationKey, StatePending, StateClaimed).
			First(&existing).Error
		if err == nil {
			return "", fmt.Errorf("%w: %s", ErrDuplicateJob, deduplicationKey)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("failed to check deduplication key: %w", err)
		}
	}

	job := Job{
		ID:               jobID,
		Queue:            queue,
		Payload:          payload,
		State:            StatePending,
		DeduplicationKey: dedup,
		Attempts:         0,
		MaxRetries:       3,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := targetDB.WithContext(ctx).Create(&job).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") ||
			strings.Contains(strings.ToLower(err.Error()), "unique") ||
			strings.Contains(err.Error(), "idx_background_jobs_active_dedup") ||
			strings.Contains(err.Error(), "idx_background_jobs_deduplication_key") {
			return "", fmt.Errorf("%w: %s", ErrDuplicateJob, deduplicationKey)
		}
		return "", fmt.Errorf("failed to insert job into ledger: %w", err)
	}

	if l.rdb != nil {
		_ = l.rdb.Publish(ctx, "jobs:notify:"+queue, jobID).Err()
	}

	return jobID, nil
}

// ClaimNext claims the oldest pending job for a given queue using SELECT ... FOR UPDATE SKIP LOCKED.
func (l *PostgresLedger) ClaimNext(ctx context.Context, queue string, workerID string, leaseDuration time.Duration) (*Job, error) {
	var job Job
	now := time.Now()
	leaseExpiresAt := now.Add(leaseDuration)

	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("queue = ? AND state = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)", queue, StatePending, now).
			Order("created_at ASC").
			Limit(1).
			Find(&job)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		job.State = StateClaimed
		job.ClaimedBy = &workerID
		job.LeaseExpiresAt = &leaseExpiresAt
		job.UpdatedAt = now

		return tx.Save(&job).Error
	})

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to claim next job: %w", err)
	}

	return &job, nil
}

// Complete marks a claimed job as completed and clears its lease expiration.
func (l *PostgresLedger) Complete(ctx context.Context, jobID string) error {
	now := time.Now()
	res := l.db.WithContext(ctx).Model(&Job{}).
		Where("id = ?", jobID).
		Updates(map[string]interface{}{
			"state":            StateCompleted,
			"lease_expires_at": nil,
			"updated_at":       now,
		})
	if res.Error != nil {
		return fmt.Errorf("failed to complete job: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrJobNotFound
	}
	return nil
}

// Release returns a claimed job back to pending state without penalizing attempts or setting errors.
func (l *PostgresLedger) Release(ctx context.Context, jobID string) error {
	now := time.Now()
	res := l.db.WithContext(ctx).Model(&Job{}).
		Where("id = ?", jobID).
		Updates(map[string]interface{}{
			"state":            StatePending,
			"claimed_by":       nil,
			"lease_expires_at": nil,
			"updated_at":       now,
		})
	if res.Error != nil {
		return fmt.Errorf("failed to release job: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrJobNotFound
	}
	return nil
}

// Fail records an error for a job, increments attempts, and transitions to failed or resets to pending.
func (l *PostgresLedger) Fail(ctx context.Context, jobID string, jobErr error, retryDelay time.Duration) error {
	errMsg := ""
	if jobErr != nil {
		errMsg = jobErr.Error()
	}

	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job Job
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", jobID).First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobNotFound
			}
			return err
		}

		job.Attempts++
		job.LastError = &errMsg
		now := time.Now()
		job.UpdatedAt = now

		if job.Attempts >= job.MaxRetries {
			job.State = StateFailed
			job.LeaseExpiresAt = nil
		} else {
			job.State = StatePending
			job.ClaimedBy = nil
			if retryDelay > 0 {
				delayUntil := now.Add(retryDelay)
				job.LeaseExpiresAt = &delayUntil
			} else {
				job.LeaseExpiresAt = nil
			}
		}

		return tx.Save(&job).Error
	})
}

// ReclaimOrphanLeases finds claimed jobs with expired leases, increments their attempts,
// and resets them to pending (or marks failed if max retries exceeded).
func (l *PostgresLedger) ReclaimOrphanLeases(ctx context.Context, queue string) (int64, error) {
	now := time.Now()
	var reclaimedCount int64

	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var orphans []Job
		query := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("state = ? AND lease_expires_at < ?", StateClaimed, now)
		if queue != "" {
			query = query.Where("queue = ?", queue)
		}

		if err := query.Find(&orphans).Error; err != nil {
			return err
		}

		for i := range orphans {
			job := &orphans[i]
			job.Attempts++
			worker := "unknown"
			if job.ClaimedBy != nil {
				worker = *job.ClaimedBy
			}
			errText := fmt.Sprintf("lease expired (claimed by: %s)", worker)
			job.LastError = &errText
			job.UpdatedAt = now

			if job.Attempts >= job.MaxRetries {
				job.State = StateFailed
				job.LeaseExpiresAt = nil
			} else {
				job.State = StatePending
				job.ClaimedBy = nil
				job.LeaseExpiresAt = nil
			}

			if err := tx.Save(job).Error; err != nil {
				return err
			}
			reclaimedCount++
		}
		return nil
	})

	return reclaimedCount, err
}

// GetJob retrieves a job record by its ID.
func (l *PostgresLedger) GetJob(ctx context.Context, jobID string) (*Job, error) {
	var job Job
	if err := l.db.WithContext(ctx).Where("id = ?", jobID).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	return &job, nil
}

// MemoryLedger is a thread-safe in-memory implementation of Ledger for unit tests and local execution.
type MemoryLedger struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

// NewMemoryLedger constructs an in-memory job ledger.
func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{
		jobs: make(map[string]*Job),
	}
}

// NewTestLedger constructs a new in-memory test ledger.
func NewTestLedger(t testing.TB) Ledger {
	return NewMemoryLedger()
}

// Enqueue inserts a job into the in-memory ledger with deduplication protection.
func (m *MemoryLedger) Enqueue(ctx context.Context, queue string, payload []byte, deduplicationKey string) (string, error) {
	return m.EnqueueTx(ctx, nil, queue, payload, deduplicationKey)
}

// EnqueueTx inserts a job into the in-memory ledger inside a transaction (noop for memory ledger).
func (m *MemoryLedger) EnqueueTx(ctx context.Context, tx *gorm.DB, queue string, payload []byte, deduplicationKey string) (string, error) {
	if queue == "" {
		return "", errors.New("queue cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if deduplicationKey != "" {
		for _, job := range m.jobs {
			if job.Queue == queue && job.DeduplicationKey != nil && *job.DeduplicationKey == deduplicationKey {
				if job.State == StatePending || job.State == StateClaimed {
					return "", fmt.Errorf("%w: %s", ErrDuplicateJob, deduplicationKey)
				}
			}
		}
	}

	jobID := uuid.New().String()
	now := time.Now()

	var dedup *string
	if deduplicationKey != "" {
		dedup = &deduplicationKey
	}

	payloadCopy := make([]byte, len(payload))
	copy(payloadCopy, payload)

	job := &Job{
		ID:               jobID,
		Queue:            queue,
		Payload:          payloadCopy,
		State:            StatePending,
		DeduplicationKey: dedup,
		Attempts:         0,
		MaxRetries:       3,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	m.jobs[jobID] = job
	return jobID, nil
}

// ClaimNext atomically claims the oldest pending job for a queue.
func (m *MemoryLedger) ClaimNext(ctx context.Context, queue string, workerID string, leaseDuration time.Duration) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var candidates []*Job
	for _, job := range m.jobs {
		if job.Queue == queue && job.State == StatePending {
			if job.LeaseExpiresAt == nil || !job.LeaseExpiresAt.After(now) {
				candidates = append(candidates, job)
			}
		}
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})

	selected := candidates[0]
	leaseExpiresAt := now.Add(leaseDuration)

	selected.State = StateClaimed
	selected.ClaimedBy = &workerID
	selected.LeaseExpiresAt = &leaseExpiresAt
	selected.UpdatedAt = now

	return copyJob(selected), nil
}

// Complete transitions a job to completed and clears lease.
func (m *MemoryLedger) Complete(ctx context.Context, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[jobID]
	if !ok {
		return ErrJobNotFound
	}

	now := time.Now()
	job.State = StateCompleted
	job.LeaseExpiresAt = nil
	job.UpdatedAt = now

	return nil
}

// Release returns a claimed job back to pending state without penalizing attempts.
func (m *MemoryLedger) Release(ctx context.Context, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[jobID]
	if !ok {
		return ErrJobNotFound
	}

	job.State = StatePending
	job.ClaimedBy = nil
	job.LeaseExpiresAt = nil
	job.UpdatedAt = time.Now()

	return nil
}

// Fail records error, increments attempts, and transitions to failed or pending.
func (m *MemoryLedger) Fail(ctx context.Context, jobID string, jobErr error, retryDelay time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[jobID]
	if !ok {
		return ErrJobNotFound
	}

	now := time.Now()
	job.Attempts++
	if jobErr != nil {
		msg := jobErr.Error()
		job.LastError = &msg
	}
	job.UpdatedAt = now

	if job.Attempts >= job.MaxRetries {
		job.State = StateFailed
		job.LeaseExpiresAt = nil
	} else {
		job.State = StatePending
		job.ClaimedBy = nil
		if retryDelay > 0 {
			delayUntil := now.Add(retryDelay)
			job.LeaseExpiresAt = &delayUntil
		} else {
			job.LeaseExpiresAt = nil
		}
	}

	return nil
}

// ReclaimOrphanLeases resets expired claimed jobs to pending or fails them if max retries exceeded.
func (m *MemoryLedger) ReclaimOrphanLeases(ctx context.Context, queue string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	var count int64

	for _, job := range m.jobs {
		if job.State == StateClaimed && job.LeaseExpiresAt != nil && job.LeaseExpiresAt.Before(now) {
			if queue == "" || job.Queue == queue {
				job.Attempts++
				worker := "unknown"
				if job.ClaimedBy != nil {
					worker = *job.ClaimedBy
				}
				errText := fmt.Sprintf("lease expired (claimed by: %s)", worker)
				job.LastError = &errText
				job.UpdatedAt = now

				if job.Attempts >= job.MaxRetries {
					job.State = StateFailed
					job.LeaseExpiresAt = nil
				} else {
					job.State = StatePending
					job.ClaimedBy = nil
					job.LeaseExpiresAt = nil
				}
				count++
			}
		}
	}

	return count, nil
}

// GetJob returns a snapshot of a job by ID.
func (m *MemoryLedger) GetJob(ctx context.Context, jobID string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[jobID]
	if !ok {
		return nil, ErrJobNotFound
	}

	return copyJob(job), nil
}

func copyJob(j *Job) *Job {
	if j == nil {
		return nil
	}

	c := *j
	if j.Payload != nil {
		c.Payload = make([]byte, len(j.Payload))
		copy(c.Payload, j.Payload)
	}
	return &c
}
