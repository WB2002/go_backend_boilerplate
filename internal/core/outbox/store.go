package outbox

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// Store provides operations to persist outbox events inside an existing or new database transaction.
type Store interface {
	CreateEvent(ctx context.Context, tx *gorm.DB, evt *Event) error
}

// GormStore implements Store with GORM.
type GormStore struct {
	db *gorm.DB
}

// NewStore constructs a new GormStore.
func NewStore(db *gorm.DB) *GormStore {
	return &GormStore{db: db}
}

// CreateEvent inserts an outbox event record. If tx is provided, it participates in that transaction.
func (s *GormStore) CreateEvent(ctx context.Context, tx *gorm.DB, evt *Event) error {
	if evt == nil {
		return errors.New("event cannot be nil")
	}

	targetDB := tx
	if targetDB == nil {
		targetDB = s.db
	}
	if targetDB == nil {
		return errors.New("database handle is nil")
	}

	return targetDB.WithContext(ctx).Create(evt).Error
}
