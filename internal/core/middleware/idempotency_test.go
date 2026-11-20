package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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

	"go_backend_boilerplate/internal/core/auth"
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

func performRequest(router *gin.Engine, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(method, path, bytes.NewBufferString(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	router.ServeHTTP(w, req)
	return w
}

func TestScopedIdempotencyKey(t *testing.T) {
	body := []byte(`{"amount":100}`)
	expectedSum := sha256.Sum256(body)
	expectedHash := hex.EncodeToString(expectedSum[:])

	cacheKey, requestHash := ScopedIdempotencyKey("user:alice", "post", "/orders/", "key-123", body)
	assert.Equal(t, "idempotency:user:alice:POST:/orders:key-123", cacheKey)
	assert.Equal(t, expectedHash, requestHash)

	anonKey, _ := ScopedIdempotencyKey("", "POST", "/orders", "key-123", nil)
	assert.Equal(t, "idempotency:anon:POST:/orders:key-123", anonKey)
}

func TestIdempotencyMiddlewareReplayAndConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mockRedis := newMockIdempotencyRedis()

	r := gin.New()
	var executionCount int64

	r.Use(Idempotency(mockRedis, 10*time.Minute))
	r.POST("/orders", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		require.Equal(t, `{"amount": 100}`, string(body))
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
	concurrentScopedKey, _ := ScopedIdempotencyKey("anon:unknown", "POST", "/orders", "concurrent-key", nil)
	mockRedis.mu.Lock()
	mockRedis.data[concurrentScopedKey] = IdempotencyStatusInProgress
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

func TestIdempotency_CrossUserAndExemptRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := newMockIdempotencyRedis()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		if u := c.GetHeader("X-User-ID"); u != "" {
			c.Set(ContextKeyUserID, u)
		}
		c.Next()
	})
	router.Use(IdempotencyWithConfig(client, time.Minute, []string{
		"/account/login",
		"/account/refresh",
		"/account/reset-password",
	}))

	var loginExecCount int64
	var refreshExecCount int64
	var resetExecCount int64
	router.POST("/account/login", func(c *gin.Context) {
		n := atomic.AddInt64(&loginExecCount, 1)
		c.JSON(http.StatusOK, gin.H{"token": "secret-jwt", "seq": n})
	})
	router.POST("/account/refresh", func(c *gin.Context) {
		n := atomic.AddInt64(&refreshExecCount, 1)
		c.JSON(http.StatusOK, gin.H{"token": "refreshed-jwt", "seq": n})
	})
	router.POST("/account/reset-password", func(c *gin.Context) {
		n := atomic.AddInt64(&resetExecCount, 1)
		c.JSON(http.StatusOK, gin.H{"status": "reset", "seq": n})
	})
	var orderExecCount int64
	router.POST("/orders", func(c *gin.Context) {
		n := atomic.AddInt64(&orderExecCount, 1)
		c.JSON(http.StatusCreated, gin.H{"orderId": n})
	})

	// 1. Ensure credential routes are NEVER cached even with Idempotency-Key
	for _, path := range []string{
		"/account/login",
		"/account/refresh",
		"/account/reset-password",
	} {
		w1 := performRequest(router, "POST", path, `{"user":"alice"}`, map[string]string{
			"Idempotency-Key": "test-key-" + path,
		})
		assert.Equal(t, http.StatusOK, w1.Code)
		assert.Empty(t, w1.Header().Get("X-Cache-Lookup"))

		w2 := performRequest(router, "POST", path, `{"user":"alice"}`, map[string]string{
			"Idempotency-Key": "test-key-" + path,
		})
		assert.Equal(t, http.StatusOK, w2.Code)
		assert.Empty(t, w2.Header().Get("X-Cache-Lookup"))
	}
	// Normalized path variants must also remain uncached in Redis
	_ = performRequest(router, "POST", "/account/login/", `{"user":"alice"}`, map[string]string{
		"Idempotency-Key": "test-key-trailing",
	})
	_ = performRequest(router, "POST", "/account//login", `{"user":"alice"}`, map[string]string{
		"Idempotency-Key": "test-key-double-slash",
	})
	assert.Equal(t, int64(2), atomic.LoadInt64(&loginExecCount))
	assert.Equal(t, int64(2), atomic.LoadInt64(&refreshExecCount))
	assert.Equal(t, int64(2), atomic.LoadInt64(&resetExecCount))
	client.mu.Lock()
	assert.Empty(t, client.data)
	client.mu.Unlock()

	// 2. Ensure User A cannot have response replayed by User B with same Idempotency-Key
	wA := performRequest(router, "POST", "/orders", `{"item":"book"}`, map[string]string{
		"Idempotency-Key": "order-key-1",
		"X-User-ID":       "user-a",
	})
	assert.Equal(t, http.StatusCreated, wA.Code)
	assert.Contains(t, wA.Body.String(), `"orderId":1`)

	// User B with same key gets a fresh execution, NOT User A's cached response
	wB := performRequest(router, "POST", "/orders", `{"item":"book"}`, map[string]string{
		"Idempotency-Key": "order-key-1",
		"X-User-ID":       "user-b",
	})
	assert.Equal(t, http.StatusCreated, wB.Code)
	assert.NotEqual(t, "true", wB.Header().Get("X-Cache-Lookup"))
	assert.Contains(t, wB.Body.String(), `"orderId":2`)

	// User A replaying with same key and same body receives cached orderId:1
	wAReplay := performRequest(router, "POST", "/orders", `{"item":"book"}`, map[string]string{
		"Idempotency-Key": "order-key-1",
		"X-User-ID":       "user-a",
	})
	assert.Equal(t, http.StatusCreated, wAReplay.Code)
	assert.Equal(t, "true", wAReplay.Header().Get("X-Cache-Lookup"))
	assert.Contains(t, wAReplay.Body.String(), `"orderId":1`)
	assert.Equal(t, int64(2), atomic.LoadInt64(&orderExecCount))
}

func TestIdempotency_PayloadMismatchAndMethodPathScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := newMockIdempotencyRedis()

	router := gin.New()
	router.Use(func(c *gin.Context) {
		if u := c.GetHeader("X-Claims-User"); u != "" {
			c.Set("user", &auth.Claims{UserID: u})
		}
		c.Next()
	})
	router.Use(IdempotencyWithConfig(client, time.Minute, nil))

	var execs int64
	router.POST("/orders", func(c *gin.Context) {
		n := atomic.AddInt64(&execs, 1)
		c.JSON(http.StatusCreated, gin.H{"exec": n})
	})
	router.PUT("/orders", func(c *gin.Context) {
		n := atomic.AddInt64(&execs, 1)
		c.JSON(http.StatusOK, gin.H{"exec": n})
	})
	router.POST("/payments", func(c *gin.Context) {
		n := atomic.AddInt64(&execs, 1)
		c.JSON(http.StatusCreated, gin.H{"exec": n})
	})

	headers := map[string]string{
		"Idempotency-Key": "shared-key",
		"X-Claims-User":   "claims-user-1",
	}

	// 1. Initial POST /orders
	w1 := performRequest(router, "POST", "/orders", `{"amount":100}`, headers)
	assert.Equal(t, http.StatusCreated, w1.Code)
	assert.Contains(t, w1.Body.String(), `"exec":1`)

	// 2. Same user, same method, same path, same key, DIFFERENT payload -> 422 Unprocessable Entity
	wMismatch := performRequest(router, "POST", "/orders", `{"amount":999}`, headers)
	assert.Equal(t, http.StatusUnprocessableEntity, wMismatch.Code)
	assert.Empty(t, wMismatch.Header().Get("X-Cache-Lookup"))
	var prob map[string]interface{}
	require.NoError(t, json.Unmarshal(wMismatch.Body.Bytes(), &prob))
	assert.Equal(t, "IDEMPOTENCY_KEY_REUSED", prob["code"])

	// 3. Same user, same key, DIFFERENT method (PUT /orders) -> fresh execution
	wMethod := performRequest(router, "PUT", "/orders", `{"amount":100}`, headers)
	assert.Equal(t, http.StatusOK, wMethod.Code)
	assert.Empty(t, wMethod.Header().Get("X-Cache-Lookup"))
	assert.Contains(t, wMethod.Body.String(), `"exec":2`)

	// 4. Same user, same key, DIFFERENT path (POST /payments) -> fresh execution
	wPath := performRequest(router, "POST", "/payments", `{"amount":100}`, headers)
	assert.Equal(t, http.StatusCreated, wPath.Code)
	assert.Empty(t, wPath.Header().Get("X-Cache-Lookup"))
	assert.Contains(t, wPath.Body.String(), `"exec":3`)
}

func TestIdempotency_AuthorizationHeaderScopingAndFailureLeaseRelease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := newMockIdempotencyRedis()

	router := gin.New()
	router.Use(IdempotencyWithConfig(client, time.Minute, nil))

	var execs int64
	router.POST("/account/change-password", func(c *gin.Context) {
		if c.GetHeader("Authorization") == "Bearer invalid" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		n := atomic.AddInt64(&execs, 1)
		c.JSON(http.StatusOK, gin.H{"exec": n})
	})

	// 1. User A with token-a
	wA := performRequest(router, "POST", "/account/change-password", `{"pw":"a"}`, map[string]string{
		"Idempotency-Key": "pw-key",
		"Authorization":   "Bearer token-a",
	})
	assert.Equal(t, http.StatusOK, wA.Code)
	assert.Contains(t, wA.Body.String(), `"exec":1`)

	// 2. User B with token-b on same IP cannot replay User A
	wB := performRequest(router, "POST", "/account/change-password", `{"pw":"a"}`, map[string]string{
		"Idempotency-Key": "pw-key",
		"Authorization":   "Bearer token-b",
	})
	assert.Equal(t, http.StatusOK, wB.Code)
	assert.Empty(t, wB.Header().Get("X-Cache-Lookup"))
	assert.Contains(t, wB.Body.String(), `"exec":2`)

	// 3. Unauthenticated / invalid bearer returns 401 and releases lease (not cached)
	w401 := performRequest(router, "POST", "/account/change-password", `{"pw":"a"}`, map[string]string{
		"Idempotency-Key": "pw-key",
		"Authorization":   "Bearer invalid",
	})
	assert.Equal(t, http.StatusUnauthorized, w401.Code)
	w401Retry := performRequest(router, "POST", "/account/change-password", `{"pw":"a"}`, map[string]string{
		"Idempotency-Key": "pw-key",
		"Authorization":   "Bearer invalid",
	})
	assert.Equal(t, http.StatusUnauthorized, w401Retry.Code)
	assert.Empty(t, w401Retry.Header().Get("X-Cache-Lookup"))
}

func TestIdempotency_DefaultExemptPathsIncludesAnonymousAccountRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := newMockIdempotencyRedis()

	router := gin.New()
	router.Use(Idempotency(client, time.Minute))

	var regCount, forgotCount int64
	router.POST("/account/register", func(c *gin.Context) {
		n := atomic.AddInt64(&regCount, 1)
		c.JSON(http.StatusCreated, gin.H{"seq": n})
	})
	router.POST("/account/forgot-password", func(c *gin.Context) {
		n := atomic.AddInt64(&forgotCount, 1)
		c.JSON(http.StatusOK, gin.H{"seq": n})
	})

	for _, tc := range []struct {
		path       string
		statusCode int
	}{
		{"/account/register", http.StatusCreated},
		{"/account/forgot-password", http.StatusOK},
	} {
		w1 := performRequest(router, http.MethodPost, tc.path, `{"email":"a@example.com"}`, map[string]string{
			"Idempotency-Key": "shared-nat-key",
		})
		assert.Equal(t, tc.statusCode, w1.Code)
		assert.Empty(t, w1.Header().Get("X-Cache-Lookup"))

		w2 := performRequest(router, http.MethodPost, tc.path, `{"email":"b@example.com"}`, map[string]string{
			"Idempotency-Key": "shared-nat-key",
		})
		assert.Equal(t, tc.statusCode, w2.Code)
		assert.Empty(t, w2.Header().Get("X-Cache-Lookup"))
	}

	assert.Equal(t, int64(2), atomic.LoadInt64(&regCount))
	assert.Equal(t, int64(2), atomic.LoadInt64(&forgotCount))
	client.mu.Lock()
	assert.Empty(t, client.data)
	client.mu.Unlock()
}
