package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Config configures the audit logging service.
type Config struct {
	BufferSize int
}

// Service defines methods for writing structured audit logs.
type Service interface {
	Log(ctx context.Context, entry Entry) error
	LogAsync(entry Entry)
	Close() error
}

type service struct {
	db    *gorm.DB
	queue chan Entry
	wg    sync.WaitGroup
	once  sync.Once
}

// NewService constructs an audit logging service.
func NewService(db *gorm.DB, cfg Config) Service {
	bufSize := cfg.BufferSize
	if bufSize <= 0 {
		bufSize = 500
	}

	s := &service{
		db:    db,
		queue: make(chan Entry, bufSize),
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for entry := range s.queue {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.Log(ctx, entry)
			cancel()
		}
	}()

	return s
}

// ScrubMap recursively redacts sensitive information like passwords and tokens.
func ScrubMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	res := make(map[string]any, len(m))
	for k, v := range m {
		lowerK := strings.ToLower(k)
		if isSensitiveKey(lowerK) {
			res[k] = "[REDACTED]"
			continue
		}
		switch val := v.(type) {
		case map[string]any:
			res[k] = ScrubMap(val)
		case []any:
			res[k] = scrubSlice(val)
		default:
			res[k] = v
		}
	}
	return res
}

func scrubSlice(s []any) []any {
	res := make([]any, len(s))
	for i, item := range s {
		switch val := item.(type) {
		case map[string]any:
			res[i] = ScrubMap(val)
		case []any:
			res[i] = scrubSlice(val)
		default:
			res[i] = item
		}
	}
	return res
}

func isSensitiveKey(k string) bool {
	sensitiveSubstrings := []string{
		"password", "token", "secret", "authorization", "api_key", "credit_card", "cvv", "private_key",
	}
	for _, sub := range sensitiveSubstrings {
		if strings.Contains(k, sub) {
			return true
		}
	}
	return k == "auth"
}

// Log writes an audit log entry synchronously.
func (s *service) Log(ctx context.Context, entry Entry) error {
	scrubbed := ScrubMap(entry.Changes)
	var changesBytes []byte
	if scrubbed != nil {
		var err error
		changesBytes, err = json.Marshal(scrubbed)
		if err != nil {
			return fmt.Errorf("failed to marshal audit changes: %w", err)
		}
	}

	status := entry.Status
	if status == "" {
		status = "success"
	}

	record := &LogRecord{
		ID:           uuid.NewString(),
		ActorID:      entry.ActorID,
		ActorEmail:   entry.ActorEmail,
		Action:       entry.Action,
		TargetType:   entry.TargetType,
		TargetID:     entry.TargetID,
		IPAddress:    entry.IPAddress,
		UserAgent:    entry.UserAgent,
		Status:       status,
		ErrorMessage: entry.ErrorMessage,
		Changes:      changesBytes,
		CreatedAt:    time.Now(),
	}

	return s.db.WithContext(ctx).Create(record).Error
}

// LogAsync queues an audit log record without blocking request execution.
func (s *service) LogAsync(entry Entry) {
	select {
	case s.queue <- entry:
	default:
		// Queue full: fail-open to never block client request
	}
}

// Close flushes the asynchronous audit logging queue and shuts down worker goroutines.
func (s *service) Close() error {
	s.once.Do(func() {
		close(s.queue)
		s.wg.Wait()
	})
	return nil
}
