package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"go_backend_boilerplate/internal/core/middleware"
	"go_backend_boilerplate/internal/core/overload"
)

func TestInboundOverloadMiddlewareIntegration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             10,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           7,
	})
	defer mgr.Stop()

	// Register route priority for /api/reports
	mgr.RegisterRoutePriority("/api/reports", overload.PriorityLow)
	mgr.RegisterRoutePriority("/api/checkout", overload.PriorityCritical)

	router := gin.New()
	router.Use(middleware.InboundOverload(mgr))

	router.GET("/api/reports", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"report": "ready"})
	})

	router.GET("/api/checkout", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "charged"})
	})

	router.GET("/api/generic", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Baseline: all pass when under load threshold
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodGet, "/api/reports", nil)
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	// Simulate load: 8 in-flight (80% of 10)
	for i := 0; i < 8; i++ {
		mgr.Acquire()
	}

	// Low priority (threshold 75%) must be rejected with 503 and Retry-After: 7
	wLow := httptest.NewRecorder()
	reqLow, _ := http.NewRequest(http.MethodGet, "/api/reports", nil)
	router.ServeHTTP(wLow, reqLow)
	assert.Equal(t, http.StatusServiceUnavailable, wLow.Code)
	assert.Equal(t, "7", wLow.Header().Get("Retry-After"))

	// Generic route defaults to PriorityNormal (threshold 90%) -> should still pass at 80%
	wGen := httptest.NewRecorder()
	reqGen, _ := http.NewRequest(http.MethodGet, "/api/generic", nil)
	router.ServeHTTP(wGen, reqGen)
	assert.Equal(t, http.StatusOK, wGen.Code)

	// Critical priority route must pass
	wCrit := httptest.NewRecorder()
	reqCrit, _ := http.NewRequest(http.MethodGet, "/api/checkout", nil)
	router.ServeHTTP(wCrit, reqCrit)
	assert.Equal(t, http.StatusOK, wCrit.Code)

	// Clean up acquired tokens
	for i := 0; i < 8; i++ {
		mgr.Release()
	}

	// Verify stats
	stats := mgr.Stats()
	assert.Equal(t, int64(10), int64(stats.MaxInFlight))
	assert.True(t, stats.TotalShed > 0)
}

func TestInboundOverloadWithPriorityMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             10,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           5,
	})
	defer mgr.Stop()

	router := gin.New()

	// Apply WithPriority before InboundOverload in route definition
	router.GET("/batch/export", middleware.WithPriority(overload.PriorityLow), middleware.InboundOverload(mgr), func(c *gin.Context) {
		c.String(http.StatusOK, "exported")
	})

	// Fill to 80%
	for i := 0; i < 8; i++ {
		mgr.Acquire()
	}
	defer func() {
		for i := 0; i < 8; i++ {
			mgr.Release()
		}
	}()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/batch/export", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "5", w.Header().Get("Retry-After"))
}

func TestInboundOverloadHeaderPriority(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mgr := overload.NewManager(overload.Config{
		MaxInFlight:             10,
		PrioritySheddingEnabled: true,
		RetryAfterSec:           5,
	})
	defer mgr.Stop()

	router := gin.New()
	router.Use(middleware.InboundOverload(mgr))
	router.GET("/items", func(c *gin.Context) {
		c.String(http.StatusOK, "items")
	})

	// Fill to 80%
	for i := 0; i < 8; i++ {
		mgr.Acquire()
	}
	defer func() {
		for i := 0; i < 8; i++ {
			mgr.Release()
		}
	}()

	// Request with X-API-Priority: low should be shed
	wLow := httptest.NewRecorder()
	reqLow, _ := http.NewRequest(http.MethodGet, "/items", nil)
	reqLow.Header.Set("X-API-Priority", "low")
	router.ServeHTTP(wLow, reqLow)
	assert.Equal(t, http.StatusServiceUnavailable, wLow.Code)

	// Request with X-API-Priority: critical should succeed
	wCrit := httptest.NewRecorder()
	reqCrit, _ := http.NewRequest(http.MethodGet, "/items", nil)
	reqCrit.Header.Set("X-API-Priority", "critical")
	router.ServeHTTP(wCrit, reqCrit)
	assert.Equal(t, http.StatusOK, wCrit.Code)
}
