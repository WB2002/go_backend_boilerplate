package ratelimit_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/ratelimit"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestRateLimitExceeded(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	limiter := ratelimit.New(store, ratelimit.Config{
		DefaultLimit:  2,
		DefaultWindow: 1 * time.Second,
	})

	router := gin.New()
	router.Use(limiter.Middleware(ratelimit.IdentifierIP, 2, 1*time.Second))
	router.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/test", nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
	}

	wBlocked := httptest.NewRecorder()
	reqBlocked, _ := http.NewRequest(http.MethodGet, "/test", nil)
	router.ServeHTTP(wBlocked, reqBlocked)
	assert.Equal(t, http.StatusTooManyRequests, wBlocked.Code)
	assert.Equal(t, "1", wBlocked.Header().Get("Retry-After"))
}

func TestRateLimitHeaders(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	limiter := ratelimit.New(store, ratelimit.Config{
		DefaultLimit:  3,
		DefaultWindow: 5 * time.Second,
	})

	router := gin.New()
	router.Use(limiter.Middleware(ratelimit.IdentifierIP, 3, 5*time.Second))
	router.GET("/headers", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	// Request 1: remaining = 2
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodGet, "/headers", nil)
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)
	assert.Equal(t, "3", w1.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "2", w1.Header().Get("X-RateLimit-Remaining"))
	reset1, err := strconv.ParseInt(w1.Header().Get("X-RateLimit-Reset"), 10, 64)
	assert.NoError(t, err)
	assert.Greater(t, reset1, time.Now().Unix()-2)
	assert.Empty(t, w1.Header().Get("Retry-After")) // No Retry-After on success

	// Request 2: remaining = 1
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/headers", nil)
	router.ServeHTTP(w2, req2)
	assert.Equal(t, "1", w2.Header().Get("X-RateLimit-Remaining"))

	// Request 3: remaining = 0
	w3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodGet, "/headers", nil)
	router.ServeHTTP(w3, req3)
	assert.Equal(t, "0", w3.Header().Get("X-RateLimit-Remaining"))

	// Request 4: Blocked (429)
	w4 := httptest.NewRecorder()
	req4, _ := http.NewRequest(http.MethodGet, "/headers", nil)
	router.ServeHTTP(w4, req4)
	assert.Equal(t, http.StatusTooManyRequests, w4.Code)
	assert.Equal(t, "3", w4.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", w4.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w4.Header().Get("X-RateLimit-Reset"))
	assert.NotEmpty(t, w4.Header().Get("Retry-After"))
	assert.Contains(t, w4.Body.String(), "rate limit exceeded")
}

func TestMultiIdentifierSelection(t *testing.T) {
	t.Run("IdentifierUserID via context key and auth.Claims", func(t *testing.T) {
		store := ratelimit.NewMemoryStore()
		limiter := ratelimit.New(store, ratelimit.Config{
			DefaultLimit:  1,
			DefaultWindow: 1 * time.Second,
		})

		router := gin.New()
		router.Use(func(c *gin.Context) {
			switch c.GetHeader("X-Test-Auth-Mode") {
			case "string":
				c.Set("user_id", c.GetHeader("X-User-ID"))
			case "claims":
				c.Set("user", &auth.Claims{UserID: c.GetHeader("X-User-ID")})
			}
			c.Next()
		})
		router.Use(limiter.Middleware(ratelimit.IdentifierUserID, 1, 1*time.Second))
		router.GET("/user-test", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		// User A (via string) - 1st allowed, 2nd blocked
		reqA1, _ := http.NewRequest(http.MethodGet, "/user-test", nil)
		reqA1.Header.Set("X-Test-Auth-Mode", "string")
		reqA1.Header.Set("X-User-ID", "user-A")
		wA1 := httptest.NewRecorder()
		router.ServeHTTP(wA1, reqA1)
		assert.Equal(t, http.StatusOK, wA1.Code)

		reqA2, _ := http.NewRequest(http.MethodGet, "/user-test", nil)
		reqA2.Header.Set("X-Test-Auth-Mode", "string")
		reqA2.Header.Set("X-User-ID", "user-A")
		wA2 := httptest.NewRecorder()
		router.ServeHTTP(wA2, reqA2)
		assert.Equal(t, http.StatusTooManyRequests, wA2.Code)

		// User B (via auth.Claims) - independent quota, allowed
		reqB1, _ := http.NewRequest(http.MethodGet, "/user-test", nil)
		reqB1.Header.Set("X-Test-Auth-Mode", "claims")
		reqB1.Header.Set("X-User-ID", "user-B")
		wB1 := httptest.NewRecorder()
		router.ServeHTTP(wB1, reqB1)
		assert.Equal(t, http.StatusOK, wB1.Code)

		// Unauthenticated request falls back to anon:IP
		reqAnon1, _ := http.NewRequest(http.MethodGet, "/user-test", nil)
		wAnon1 := httptest.NewRecorder()
		router.ServeHTTP(wAnon1, reqAnon1)
		assert.Equal(t, http.StatusOK, wAnon1.Code)

		reqAnon2, _ := http.NewRequest(http.MethodGet, "/user-test", nil)
		wAnon2 := httptest.NewRecorder()
		router.ServeHTTP(wAnon2, reqAnon2)
		assert.Equal(t, http.StatusTooManyRequests, wAnon2.Code)
	})

	t.Run("IdentifierDeviceFingerprint", func(t *testing.T) {
		store := ratelimit.NewMemoryStore()
		limiter := ratelimit.New(store, ratelimit.Config{
			DefaultLimit:  1,
			DefaultWindow: 1 * time.Second,
		})

		router := gin.New()
		router.Use(limiter.Middleware(ratelimit.IdentifierDeviceFingerprint, 1, 1*time.Second))
		router.GET("/fp-test", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		// Device 1
		reqD1, _ := http.NewRequest(http.MethodGet, "/fp-test", nil)
		reqD1.Header.Set("X-Device-Fingerprint", "device-alpha")
		wD1 := httptest.NewRecorder()
		router.ServeHTTP(wD1, reqD1)
		assert.Equal(t, http.StatusOK, wD1.Code)

		reqD1Blocked, _ := http.NewRequest(http.MethodGet, "/fp-test", nil)
		reqD1Blocked.Header.Set("X-Device-Fingerprint", "device-alpha")
		wD1Blocked := httptest.NewRecorder()
		router.ServeHTTP(wD1Blocked, reqD1Blocked)
		assert.Equal(t, http.StatusTooManyRequests, wD1Blocked.Code)

		// Device 2 has separate bucket
		reqD2, _ := http.NewRequest(http.MethodGet, "/fp-test", nil)
		reqD2.Header.Set("X-Device-Fingerprint", "device-beta")
		wD2 := httptest.NewRecorder()
		router.ServeHTTP(wD2, reqD2)
		assert.Equal(t, http.StatusOK, wD2.Code)
	})

	t.Run("IdentifierGuestSession", func(t *testing.T) {
		store := ratelimit.NewMemoryStore()
		limiter := ratelimit.New(store, ratelimit.Config{
			DefaultLimit:  1,
			DefaultWindow: 1 * time.Second,
		})

		router := gin.New()
		router.Use(limiter.Middleware(ratelimit.IdentifierGuestSession, 1, 1*time.Second))
		router.GET("/guest-test", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		// Guest A
		reqG1, _ := http.NewRequest(http.MethodGet, "/guest-test", nil)
		reqG1.AddCookie(&http.Cookie{Name: "guest_session", Value: "guest-token-1"})
		wG1 := httptest.NewRecorder()
		router.ServeHTTP(wG1, reqG1)
		assert.Equal(t, http.StatusOK, wG1.Code)

		// Guest A blocked
		reqG1Blocked, _ := http.NewRequest(http.MethodGet, "/guest-test", nil)
		reqG1Blocked.AddCookie(&http.Cookie{Name: "guest_session", Value: "guest-token-1"})
		wG1Blocked := httptest.NewRecorder()
		router.ServeHTTP(wG1Blocked, reqG1Blocked)
		assert.Equal(t, http.StatusTooManyRequests, wG1Blocked.Code)

		// Guest B allowed
		reqG2, _ := http.NewRequest(http.MethodGet, "/guest-test", nil)
		reqG2.AddCookie(&http.Cookie{Name: "guest_session", Value: "guest-token-2"})
		wG2 := httptest.NewRecorder()
		router.ServeHTTP(wG2, reqG2)
		assert.Equal(t, http.StatusOK, wG2.Code)
	})
}

func TestSlidingWindowPruning(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	limiter := ratelimit.New(store, ratelimit.Config{
		DefaultLimit:  2,
		DefaultWindow: 60 * time.Millisecond,
	})

	ctx := context.Background()

	// 1st request
	res, err := limiter.Allow(ctx, "test-key", 2, 60*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 1, res.Remaining)

	// 2nd request
	res, err = limiter.Allow(ctx, "test-key", 2, 60*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 0, res.Remaining)

	// 3rd request: blocked
	res, err = limiter.Allow(ctx, "test-key", 2, 60*time.Millisecond)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Greater(t, res.RetryAfter, time.Duration(0))

	// Sleep until window expires
	time.Sleep(70 * time.Millisecond)

	// 4th request: sliding window expired oldest requests, now permitted
	res, err = limiter.Allow(ctx, "test-key", 2, 60*time.Millisecond)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
}

func TestMemoryStoreLRUEviction(t *testing.T) {
	// Create store with max 2 entries
	store := ratelimit.NewMemoryStore(ratelimit.WithMaxEntries(2))
	ctx := context.Background()

	// Add key1 and key2
	_, err := store.Allow(ctx, "key1", 5, time.Minute)
	require.NoError(t, err)
	_, err = store.Allow(ctx, "key2", 5, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 2, store.Len())

	// Touch key1, making key2 the LRU
	_, err = store.Allow(ctx, "key1", 5, time.Minute)
	require.NoError(t, err)

	// Add key3: exceeds capacity 2, so key2 must be evicted
	_, err = store.Allow(ctx, "key3", 5, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 2, store.Len())

	// Verify key1 is still present (counter continues from 2)
	res1, err := store.Allow(ctx, "key1", 5, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 2, res1.Remaining) // had 2 earlier + 1 now = 3, remaining = 5-3=2

	// Clear
	store.Clear()
	assert.Equal(t, 0, store.Len())
}

// mockFailingStore simulates a primary store that can fail or succeed on demand.
type mockFailingStore struct {
	failCount  atomic.Int64
	totalCalls atomic.Int64
}

func (m *mockFailingStore) Allow(ctx context.Context, key string, limit int, window time.Duration) (*ratelimit.Result, error) {
	m.totalCalls.Add(1)
	if m.failCount.Load() > 0 {
		m.failCount.Add(-1)
		return nil, errors.New("simulated redis network partition")
	}
	return &ratelimit.Result{
		Allowed:   true,
		Limit:     limit,
		Remaining: limit - 1,
		Reset:     window,
	}, nil
}

func TestRedisFailureFallbackToMemoryAndCooldown(t *testing.T) {
	primaryMock := &mockFailingStore{}
	memFallback := ratelimit.NewMemoryStore()

	cooldown := 50 * time.Millisecond
	fallbackStore := ratelimit.NewFallbackStore(primaryMock, memFallback, cooldown)
	limiter := ratelimit.New(fallbackStore, ratelimit.Config{
		DefaultLimit:     2,
		DefaultWindow:    1 * time.Second,
		FallbackCooldown: cooldown,
	})

	ctx := context.Background()

	// 1. Initially primary succeeds
	res, err := limiter.Allow(ctx, "client-1", 2, 1*time.Second)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.False(t, fallbackStore.IsInCooldown())
	assert.Equal(t, int64(1), primaryMock.totalCalls.Load())

	// 2. Simulate Redis failure for 1 call
	primaryMock.failCount.Store(1)

	// Call will hit primary, fail, activate cooldown, and fall back to memory
	res, err = limiter.Allow(ctx, "client-1", 2, 1*time.Second)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.True(t, fallbackStore.IsInCooldown())
	assert.Equal(t, int64(2), primaryMock.totalCalls.Load()) // Called primary once

	// 3. Next call during cooldown bypasses primary completely (proves cooldown protection)
	res, err = limiter.Allow(ctx, "client-1", 2, 1*time.Second)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, int64(2), primaryMock.totalCalls.Load()) // Primary was NOT called!
	assert.True(t, fallbackStore.IsInCooldown())

	// 4. Wait for cooldown to expire
	time.Sleep(60 * time.Millisecond)
	assert.False(t, fallbackStore.IsInCooldown())

	// 5. Next call after cooldown probes primary again (cooldown recovery)
	res, err = limiter.Allow(ctx, "client-1", 2, 1*time.Second)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, int64(3), primaryMock.totalCalls.Load()) // Primary probed again
	assert.False(t, fallbackStore.IsInCooldown())            // Recovered!
}

func TestRedisStoreNilClientReturnsError(t *testing.T) {
	redisStore := ratelimit.NewRedisStore(nil)
	res, err := redisStore.Allow(context.Background(), "test", 5, time.Second)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ratelimit.ErrNilClient)
}

func TestLimiterFromConfig(t *testing.T) {
	cfg := config.RateLimitConfig{
		DefaultLimit:       50,
		DefaultWindowSec:   30,
		FallbackCooldownMs: 2000,
	}

	// Without Redis client: falls back to in-memory store
	limiter := ratelimit.NewFromConfig(cfg, nil)
	require.NotNil(t, limiter)

	res, err := limiter.Allow(context.Background(), "cfg-test", 0, 0)
	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, 50, res.Limit)
}

func TestRouteScopedLimiter(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	limiter := ratelimit.New(store, ratelimit.Config{
		DefaultLimit:  1,
		DefaultWindow: 1 * time.Second,
	})

	router := gin.New()
	// Scoped to route
	router.GET("/route-a", limiter.Middleware(ratelimit.IdentifierIP, 1, time.Second, ratelimit.WithRouteScope(true)), func(c *gin.Context) {
		c.String(http.StatusOK, "a")
	})
	router.GET("/route-b", limiter.Middleware(ratelimit.IdentifierIP, 1, time.Second, ratelimit.WithRouteScope(true)), func(c *gin.Context) {
		c.String(http.StatusOK, "b")
	})

	// /route-a Request 1: allowed
	wA1 := httptest.NewRecorder()
	reqA1, _ := http.NewRequest(http.MethodGet, "/route-a", nil)
	router.ServeHTTP(wA1, reqA1)
	assert.Equal(t, http.StatusOK, wA1.Code)

	// /route-a Request 2: blocked
	wA2 := httptest.NewRecorder()
	reqA2, _ := http.NewRequest(http.MethodGet, "/route-a", nil)
	router.ServeHTTP(wA2, reqA2)
	assert.Equal(t, http.StatusTooManyRequests, wA2.Code)

	// /route-b Request 1: allowed (isolated route bucket)
	wB1 := httptest.NewRecorder()
	reqB1, _ := http.NewRequest(http.MethodGet, "/route-b", nil)
	router.ServeHTTP(wB1, reqB1)
	assert.Equal(t, http.StatusOK, wB1.Code)
}
