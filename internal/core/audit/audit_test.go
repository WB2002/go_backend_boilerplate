package audit

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestAuditService_NilDBNoPanic(t *testing.T) {
	svc := NewService(nil, Config{BufferSize: 10})
	defer svc.Close()

	// Should not panic on Log or LogAsync
	assert.NotPanics(t, func() {
		err := svc.Log(context.Background(), Entry{
			Action:    "test.action",
			TargetID:  "oversized-target-id-exceeding-standard-length-bounds-abcdefghijklmnopqrstuvwxyz1234567890",
			IPAddress: strings.Repeat("192.168.1.1,", 10),
		})
		assert.NoError(t, err)
		svc.LogAsync(Entry{Action: "async.test"})
	})
}

func TestAuditService_InputBoundingTruncation(t *testing.T) {
	db := setupTestDB(t)
	svc := NewService(db, Config{BufferSize: 10})
	defer svc.Close()

	// Multi-byte UTF-8 runes exceeding 64 and 45 runes respectively
	oversizedTargetID := strings.Repeat("ü", 80)
	oversizedIP := strings.Repeat("é", 60)

	err := svc.Log(context.Background(), Entry{
		ActorID:    "usr-bound-1",
		Action:     "resource.update",
		TargetType: "document",
		TargetID:   oversizedTargetID,
		IPAddress:  oversizedIP,
	})
	require.NoError(t, err)

	var record LogRecord
	err = db.First(&record, "actor_id = ?", "usr-bound-1").Error
	require.NoError(t, err)

	assert.Equal(t, 64, utf8.RuneCountInString(record.TargetID))
	assert.True(t, utf8.ValidString(record.TargetID))
	assert.Equal(t, 45, utf8.RuneCountInString(record.IPAddress))
	assert.True(t, utf8.ValidString(record.IPAddress))
}

func TestAuditService_WorkerPanicRecovery(t *testing.T) {
	// Uninitialized *gorm.DB is non-nil but panics inside WithContext/Create.
	brokenDB := &gorm.DB{}
	svc := NewService(brokenDB, Config{BufferSize: 4})

	svc.LogAsync(Entry{
		ActorID: "usr-panic-1",
		Action:  "panic.test",
	})
	svc.LogAsync(Entry{
		ActorID: "usr-panic-2",
		Action:  "panic.test.after",
	})

	// Close must complete cleanly without hanging if the worker recovered properly.
	done := make(chan struct{})
	go func() {
		_ = svc.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close() timed out; worker goroutine may not have recovered cleanly")
	}
}

func TestAuditService_LogAsyncFullQueueDoesNotBlock(t *testing.T) {
	svc := NewService(nil, Config{BufferSize: 1})
	defer svc.Close()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			svc.LogAsync(Entry{Action: "queue.overflow.test"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("LogAsync blocked when queue was full")
	}
}
