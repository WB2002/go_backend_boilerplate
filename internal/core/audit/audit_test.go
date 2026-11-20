package audit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	err = db.AutoMigrate(&LogRecord{})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM audit_logs").Error
	})
	_ = db.Exec("DELETE FROM audit_logs").Error
	return db
}

func TestAuditLogPersistenceAndScrubbing(t *testing.T) {
	db := setupTestDB(t)

	service := NewService(db, Config{
		BufferSize: 100,
	})
	defer service.Close()

	ctx := context.Background()

	entry := Entry{
		ActorID:    "usr-admin-1",
		ActorEmail: "admin@example.com",
		Action:     "user.password_reset",
		TargetType: "user",
		TargetID:   "usr-target-2",
		IPAddress:  "192.168.1.1",
		UserAgent:  "Mozilla/5.0",
		Status:     "success",
		Changes: map[string]any{
			"password":     "SuperSecretPlainPassword123!",
			"accessToken":  "jwt.token.string",
			"target_email": "target@example.com",
			"role":         "editor",
		},
	}

	err := service.Log(ctx, entry)
	assert.NoError(t, err)

	var record LogRecord
	err = db.First(&record, "actor_id = ?", "usr-admin-1").Error
	require.NoError(t, err)

	assert.Equal(t, "user.password_reset", record.Action)
	assert.Equal(t, "192.168.1.1", record.IPAddress)

	// Verify sensitive fields are masked in JSON payload
	changesJSON := string(record.Changes)
	assert.NotContains(t, changesJSON, "SuperSecretPlainPassword123!")
	assert.NotContains(t, changesJSON, "jwt.token.string")
	assert.Contains(t, changesJSON, "[REDACTED]")
	assert.Contains(t, changesJSON, "target@example.com")
}

func TestAuditLogAsyncDrain(t *testing.T) {
	db := setupTestDB(t)

	service := NewService(db, Config{
		BufferSize: 10,
	})
	defer service.Close()

	entry := Entry{
		ActorID:    "usr-async-1",
		ActorEmail: "async@example.com",
		Action:     "account.login",
		TargetType: "user",
		TargetID:   "usr-async-1",
		IPAddress:  "10.0.0.1",
		UserAgent:  "curl/7.68.0",
		Status:     "success",
	}

	service.LogAsync(entry)

	// Allow worker goroutine to persist
	assert.Eventually(t, func() bool {
		var count int64
		db.Model(&LogRecord{}).Where("actor_id = ?", "usr-async-1").Count(&count)
		return count == 1
	}, 1*time.Second, 20*time.Millisecond)
}
