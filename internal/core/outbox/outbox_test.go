package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"go_backend_boilerplate/internal/platform/database"
)

func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	err = db.AutoMigrate(&Event{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM outbox_events").Error
	})
	_ = db.Exec("DELETE FROM outbox_events").Error
	return db
}

func TestTransactionalOutboxRollback(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	ctx := context.Background()

	// 1. Transaction that fails and rolls back
	err := database.WithTransaction(ctx, db, func(tx *gorm.DB) error {
		evt := &Event{
			ID:            uuid.NewString(),
			AggregateType: "user",
			AggregateID:   "usr-123",
			EventType:     "user.created",
			Payload:       []byte(`{"email":"test@example.com"}`),
			State:         StatePending,
		}
		if err := store.CreateEvent(ctx, tx, evt); err != nil {
			return err
		}
		return errors.New("simulated business error")
	})
	assert.Error(t, err)

	// Verify no outbox record exists
	var count int64
	db.Model(&Event{}).Count(&count)
	assert.Equal(t, int64(0), count, "Outbox event must be rolled back on error")

	// 2. Transaction that succeeds and commits
	err = database.WithTransaction(ctx, db, func(tx *gorm.DB) error {
		evt := &Event{
			ID:            uuid.NewString(),
			AggregateType: "user",
			AggregateID:   "usr-456",
			EventType:     "user.created",
			Payload:       []byte(`{"email":"committed@example.com"}`),
			State:         StatePending,
		}
		return store.CreateEvent(ctx, tx, evt)
	})
	assert.NoError(t, err)

	db.Model(&Event{}).Count(&count)
	assert.Equal(t, int64(1), count, "Outbox event must persist on successful commit")
}
