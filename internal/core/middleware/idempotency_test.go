package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockIdempotencyRedis is an in-memory thread-safe mock for goredis.Cmdable.
type mockIdempotencyRedis struct {
	goredis.Cmdable
	mu   sync.Mutex
	data map[string]string
}

func newMockIdempotencyRedis() *mockIdempotencyRedis {
	return &mockIdempotencyRedis{
		data: make(map[string]string),
	}
}

func (m *mockIdempotencyRedis) SetNX(ctx context.Context, key string, value any, expiration time.Duration) *goredis.BoolCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := goredis.NewBoolCmd(ctx)
	if _, exists := m.data[key]; exists {
		cmd.SetVal(false)
		return cmd
	}
	m.data[key] = value.(string)
	cmd.SetVal(true)
	return cmd
}

func (m *mockIdempotencyRedis) Get(ctx context.Context, key string) *goredis.StringCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := goredis.NewStringCmd(ctx)
	val, ok := m.data[key]
	if !ok {
		cmd.SetErr(goredis.Nil)
		return cmd
	}
	cmd.SetVal(val)
	return cmd
}

func (m *mockIdempotencyRedis) Set(ctx context.Context, key string, value any, expiration time.Duration) *goredis.StatusCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := goredis.NewStatusCmd(ctx)
	m.data[key] = value.(string)
	cmd.SetVal("OK")
	return cmd
}

func (m *mockIdempotencyRedis) Del(ctx context.Context, keys ...string) *goredis.IntCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := goredis.NewIntCmd(ctx)
	var count int64
	for _, k := range keys {
		if _, ok := m.data[k]; ok {
			delete(m.data, k)
			count++
		}
	}
	cmd.SetVal(count)
	return cmd
}

func TestIdempotencyMiddlewareReplayAndConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mockRedis := newMockIdempotencyRedis()

	r := gin.New()
	var executionCount int64

	r.Use(Idempotency(mockRedis, 10*time.Minute))
	r.POST("/orders", func(c *gin.Context) {
		atomic.AddInt64(&executionCount, 1)
		c.JSON(http.StatusCreated, gin.H{"orderId": "ord-12345", "status": "processed"})
	})

	idempotencyKey := "req-uuid-abc-123"

	// 1. Initial request execution
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("POST", "/orders", bytes.NewReader([]byte(`{"amount": 100}`)))
	req1.Header.Set("Idempotency-Key", idempotencyKey)
	r.ServeHTTP(w1, req1)

	assert.Equal(t, http.StatusCreated, w1.Code)
	assert.Contains(t, w1.Body.String(), "ord-12345")
	assert.Equal(t, int64(1), atomic.LoadInt64(&executionCount))

	// 2. Exact duplicate request replay
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("POST", "/orders", bytes.NewReader([]byte(`{"amount": 100}`)))
	req2.Header.Set("Idempotency-Key", idempotencyKey)
	r.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusCreated, w2.Code)
	assert.Equal(t, w1.Body.String(), w2.Body.String())
	assert.Equal(t, "true", w2.Header().Get("X-Cache-Lookup"))
	// Handler must NOT have executed a second time
	assert.Equal(t, int64(1), atomic.LoadInt64(&executionCount))

	// 3. Test concurrent in-flight request returns 409 Conflict
	mockRedis.mu.Lock()
	mockRedis.data["idempotency:concurrent-key"] = "IN_PROGRESS"
	mockRedis.mu.Unlock()

	wConflict := httptest.NewRecorder()
	reqConflict, _ := http.NewRequest("POST", "/orders", bytes.NewReader([]byte(`{"amount": 100}`)))
	reqConflict.Header.Set("Idempotency-Key", "concurrent-key")
	r.ServeHTTP(wConflict, reqConflict)

	assert.Equal(t, http.StatusConflict, wConflict.Code)
	var conflictBody map[string]interface{}
	err := json.Unmarshal(wConflict.Body.Bytes(), &conflictBody)
	require.NoError(t, err)
	assert.Equal(t, "CONCURRENT_REQUEST", conflictBody["code"])
}
