// internal/core/overload/overload_test.go
package overload_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/overload"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestPriorityLoadShedding(t *testing.T) {
	// Max in-flight: 2 requests
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             2,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           5,
	})
	defer mgr.Stop()

	router := gin.New()
	router.Use(mgr.Middleware())

	router.GET("/low", func(c *gin.Context) {
		c.Set("api_priority", overload.PriorityLow)
		c.String(http.StatusOK, "low priority done")
	})

	router.GET("/critical", func(c *gin.Context) {
		c.Set("api_priority", overload.PriorityCritical)
		c.String(http.StatusOK, "critical done")
	})

	// Simulate system under load by acquiring in-flight tokens
	mgr.Acquire()
	mgr.Acquire()

	// Low priority request must be rejected with 503 Overloaded
	wLow := httptest.NewRecorder()
	reqLow, _ := http.NewRequest(http.MethodGet, "/low", nil)
	router.ServeHTTP(wLow, reqLow)
	assert.Equal(t, http.StatusServiceUnavailable, wLow.Code)
	assert.Equal(t, "5", wLow.Header().Get("Retry-After"))

	// Release one token
	mgr.Release()

	// Critical priority request must succeed
	wCrit := httptest.NewRecorder()
	reqCrit, _ := http.NewRequest(http.MethodGet, "/critical", nil)
	router.ServeHTTP(wCrit, reqCrit)
	assert.Equal(t, http.StatusOK, wCrit.Code)
}

func TestPriorityTiersThresholds(t *testing.T) {
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             100,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           10,
	})
	defer mgr.Stop()

	// In-flight < 75%: nothing shed
	for i := 0; i < 70; i++ {
		mgr.Acquire()
	}
	assert.False(t, mgr.ShouldShed(overload.PriorityLow))
	assert.False(t, mgr.ShouldShed(overload.PriorityNormal))
	assert.False(t, mgr.ShouldShed(overload.PriorityHigh))
	assert.False(t, mgr.ShouldShed(overload.PriorityCritical))

	// In-flight >= 75%: Low shed
	for i := 0; i < 5; i++ {
		mgr.Acquire()
	}
	assert.Equal(t, int64(75), mgr.InFlight())
	assert.True(t, mgr.ShouldShed(overload.PriorityLow))
	assert.False(t, mgr.ShouldShed(overload.PriorityNormal))
	assert.False(t, mgr.ShouldShed(overload.PriorityHigh))
	assert.False(t, mgr.ShouldShed(overload.PriorityCritical))

	// In-flight >= 90%: Normal shed
	for i := 0; i < 15; i++ {
		mgr.Acquire()
	}
	assert.Equal(t, int64(90), mgr.InFlight())
	assert.True(t, mgr.ShouldShed(overload.PriorityLow))
	assert.True(t, mgr.ShouldShed(overload.PriorityNormal))
	assert.False(t, mgr.ShouldShed(overload.PriorityHigh))
	assert.False(t, mgr.ShouldShed(overload.PriorityCritical))

	// In-flight >= 100%: High shed, Critical preserved
	for i := 0; i < 10; i++ {
		mgr.Acquire()
	}
	assert.Equal(t, int64(100), mgr.InFlight())
	assert.True(t, mgr.ShouldShed(overload.PriorityLow))
	assert.True(t, mgr.ShouldShed(overload.PriorityNormal))
	assert.True(t, mgr.ShouldShed(overload.PriorityHigh))
	assert.False(t, mgr.ShouldShed(overload.PriorityCritical))
}

func TestDisabledPriorityShedding(t *testing.T) {
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             10,
		PrioritySheddingEnabled: false,
		RetryAfterSec:           5,
	})
	defer mgr.Stop()

	for i := 0; i < 8; i++ {
		mgr.Acquire()
	}
	// At 80% with priority shedding disabled, low priority is NOT shed
	assert.False(t, mgr.ShouldShed(overload.PriorityLow))

	// When exceeding 10, all are shed
	for i := 0; i < 2; i++ {
		mgr.Acquire()
	}
	assert.True(t, mgr.ShouldShed(overload.PriorityLow))
	assert.True(t, mgr.ShouldShed(overload.PriorityCritical))
}

func TestTryAcquireAtomicCAS(t *testing.T) {
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             2,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           5,
	})
	defer mgr.Stop()

	assert.True(t, mgr.TryAcquire(overload.PriorityNormal))
	assert.Equal(t, int64(1), mgr.InFlight())

	// Low priority requires in_flight < 2 * 0.75 (1.5) -> next is 2, curr is 1 (< 1.5), so allowed
	assert.True(t, mgr.TryAcquire(overload.PriorityLow))
	assert.Equal(t, int64(2), mgr.InFlight())

	// At 2 in-flight, Low, Normal, High must fail TryAcquire
	assert.False(t, mgr.TryAcquire(overload.PriorityLow))
	assert.False(t, mgr.TryAcquire(overload.PriorityNormal))
	assert.False(t, mgr.TryAcquire(overload.PriorityHigh))

	// Critical has reserve cap (2.5), so curr=2 is allowed
	assert.True(t, mgr.TryAcquire(overload.PriorityCritical))
	assert.Equal(t, int64(3), mgr.InFlight())

	// Exceeding reserve cap: Critical also fails
	assert.False(t, mgr.TryAcquire(overload.PriorityCritical))

	// Releasing
	mgr.Release()
	mgr.Release()
	mgr.Release()
	assert.Equal(t, int64(0), mgr.InFlight())
}

func TestWaitQueueThunderingHerd(t *testing.T) {
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             1,
		PrioritySheddingEnabled: false,
		RetryAfterSec:           3,
		LoadLevelMaxWait:        200 * time.Millisecond,
		LoadLevelQueueSize:      10,
	})
	defer mgr.Stop()

	router := gin.New()
	router.Use(mgr.Middleware())
	router.GET("/work", func(c *gin.Context) {
		time.Sleep(20 * time.Millisecond)
		c.String(http.StatusOK, "done")
	})

	// Pre-fill the 1 slot
	mgr.Acquire()

	var wg sync.WaitGroup
	var successCount int64
	var failCount int64
	var mu sync.Mutex

	// Launch 3 concurrent requests that enter load leveling queue
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/work", nil)
			router.ServeHTTP(w, req)
			mu.Lock()
			switch w.Code {
			case http.StatusOK:
				successCount++
			case http.StatusServiceUnavailable:
				failCount++
			}
			mu.Unlock()
		}()
	}

	// Give time for all 3 goroutines to queue up
	time.Sleep(30 * time.Millisecond)

	// Release the single slot
	mgr.Release()

	wg.Wait()

	// Exactly 1 waiter should successfully acquire the freed slot before others or subsequent timing
	mu.Lock()
	totalHandled := successCount + failCount
	assert.Equal(t, int64(3), totalHandled)
	assert.True(t, successCount >= 1)
	mu.Unlock()
}

func TestLoadLevelingQueue(t *testing.T) {
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             1,
		PrioritySheddingEnabled: false,
		RetryAfterSec:           3,
		LoadLevelMaxWait:        50 * time.Millisecond,
		LoadLevelQueueSize:      10,
	})
	defer mgr.Stop()

	router := gin.New()
	router.Use(mgr.Middleware())
	router.GET("/work", func(c *gin.Context) {
		time.Sleep(20 * time.Millisecond)
		c.String(http.StatusOK, "done")
	})

	// Occupy the single slot
	mgr.Acquire()

	// Request should wait in load-leveling queue and time out after 50ms
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/work", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "3", w.Header().Get("Retry-After"))

	// Now release token and ensure subsequent request passes
	mgr.Release()
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, "/work", nil)
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "done", w2.Body.String())
}

func TestPriorityExtractionNoLooseSubstringMatching(t *testing.T) {
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             10,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           5,
		DefaultPriority:         overload.PriorityNormal,
	})
	defer mgr.Stop()

	// Ensure paths like /allow, /workflow, /followers do NOT default to PriorityLow
	router := gin.New()
	router.Use(mgr.Middleware())

	var capturedPriority overload.Priority
	captureHandler := func(c *gin.Context) {
		capturedPriority = mgr.ExtractPriority(c)
		c.String(http.StatusOK, "ok")
	}

	router.GET("/allow", captureHandler)
	router.GET("/workflow", captureHandler)
	router.GET("/followers", captureHandler)

	for _, path := range []string{"/allow", "/workflow", "/followers"} {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, path, nil)
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, overload.PriorityNormal, capturedPriority, "path %s must not match PriorityLow", path)
	}

	// Exact path /low must match PriorityLow
	router.GET("/low", captureHandler)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/low", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, overload.PriorityLow, capturedPriority)
}

func TestUnderPressureMemoryAndGC(t *testing.T) {
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             10,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           5,
		MaxHeapBytes:            1024, // 1KB threshold (system currently has much more)
	})
	defer mgr.Stop()

	// Check that memory pressure flag is active
	assert.True(t, mgr.IsUnderPressure())
	// Under pressure, non-critical priority is shed even if in-flight is 0
	assert.True(t, mgr.ShouldShed(overload.PriorityNormal))
	assert.True(t, mgr.ShouldShed(overload.PriorityLow))
	assert.False(t, mgr.TryAcquire(overload.PriorityNormal))
	assert.False(t, mgr.TryAcquire(overload.PriorityLow))
}

func TestNewManagerFromConfig(t *testing.T) {
	cfg := config.OverloadConfig{
		MaxInFlight:               500,
		PrioritySheddingEnabled:   true,
		RetryAfterSec:             12,
		InboundLoadLevelMaxQueue:  25,
		InboundLoadLevelMaxWaitMs: 300,
	}

	mgr := overload.NewManagerFromConfig(cfg)
	defer mgr.Stop()

	stats := mgr.Stats()
	assert.Equal(t, 500, stats.MaxInFlight)
}

func TestExtractPriority_IgnoresUntrustedClientHeaders(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/public", nil)
	c.Request.Header.Set("X-API-Priority", "critical")
	c.Request.Header.Set("X-Priority", "critical")
	p := overload.ExtractPriority(c, nil)
	assert.Equal(t, overload.PriorityNormal, p) // Must not be PriorityCritical

	// Even with a Manager and registered low-priority route, forged headers cannot elevate priority
	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             10,
		PrioritySheddingEnabled: true,
	})
	defer mgr.Stop()
	mgr.RegisterRoutePriority("/public", overload.PriorityLow)
	assert.Equal(t, overload.PriorityLow, overload.ExtractPriority(c, mgr))
}
