package outbox

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RelayConfig holds configuration parameters for the outbox relay daemon.
type RelayConfig struct {
	BatchSize    int
	PollInterval time.Duration
	MaxRetries   int
}

// Relay polls pending outbox records and publishes them to the message broker.
type Relay struct {
	db        *gorm.DB
	publisher Publisher
	cfg       RelayConfig
}

// NewRelay constructs an autonomous outbox relay processor.
func NewRelay(db *gorm.DB, publisher Publisher, cfg RelayConfig) *Relay {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 100 * time.Millisecond
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}

	return &Relay{
		db:        db,
		publisher: publisher,
		cfg:       cfg,
	}
}

// ProcessBatch claims and dispatches a single batch of pending outbox events.
func (r *Relay) ProcessBatch(ctx context.Context) (int, error) {
	var events []Event

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("state = ?", StatePending).
			Order("created_at ASC").
			Limit(r.cfg.BatchSize).
			Find(&events)
		if res.Error != nil {
			return res.Error
		}

		for i := range events {
			evt := &events[i]
			pubErr := r.publisher.Publish(ctx, evt)
			if pubErr == nil {
				now := time.Now()
				if err := tx.Model(evt).Updates(map[string]any{
					"state":        StatePublished,
					"published_at": &now,
				}).Error; err != nil {
					return err
				}
			} else {
				retries := evt.RetryCount + 1
				newState := StatePending
				if retries >= r.cfg.MaxRetries {
					newState = StateFailed
				}
				errMsg := pubErr.Error()
				if err := tx.Model(evt).Updates(map[string]any{
					"retry_count": retries,
					"state":       newState,
					"last_error":  &errMsg,
				}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})

	if err != nil {
		return 0, err
	}
	return len(events), nil
}

// Start launches the polling relay daemon, blocking until the context is canceled.
func (r *Relay) Start(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	// Drain any backlog on startup
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		n, err := r.ProcessBatch(ctx)
		if err != nil || n < r.cfg.BatchSize {
			break
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			for {
				if ctx.Err() != nil {
					return ctx.Err()
				}

				n, err := r.ProcessBatch(ctx)
				if err != nil || n < r.cfg.BatchSize {
					break
				}
			}
		}
	}
}
