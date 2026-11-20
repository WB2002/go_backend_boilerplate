package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"go_backend_boilerplate/internal/core/middleware"
	"go_backend_boilerplate/internal/core/ratelimit"
)

func TestRateLimitMiddlewareAdapters(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("RateLimitWithLimiter", func(t *testing.T) {
		store := ratelimit.NewMemoryStore()
		limiter := ratelimit.New(store, ratelimit.Config{
			DefaultLimit:  2,
			DefaultWindow: time.Second,
		})

		router := gin.New()
		router.Use(middleware.RateLimitWithLimiter(limiter, ratelimit.IdentifierIP, 2, time.Second))
		router.GET("/adapter", func(c *gin.Context) {
			c.String(http.StatusOK, "adapter ok")
		})

		for i := 0; i < 2; i++ {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/adapter", nil)
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code)
		}

		wBlocked := httptest.NewRecorder()
		reqBlocked, _ := http.NewRequest(http.MethodGet, "/adapter", nil)
		router.ServeHTTP(wBlocked, reqBlocked)
		assert.Equal(t, http.StatusTooManyRequests, wBlocked.Code)
		assert.Equal(t, "1", wBlocked.Header().Get("Retry-After"))
	})

	t.Run("DefaultRateLimitMiddleware", func(t *testing.T) {
		customStore := ratelimit.NewMemoryStore()
		customLimiter := ratelimit.New(customStore, ratelimit.Config{
			DefaultLimit:  1,
			DefaultWindow: time.Second,
		})
		middleware.SetDefaultLimiter(customLimiter)
		assert.Equal(t, customLimiter, middleware.DefaultLimiter())

		router := gin.New()
		router.Use(middleware.RateLimit(ratelimit.IdentifierIP, 1, time.Second))
		router.GET("/default-adapter", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w1 := httptest.NewRecorder()
		req1, _ := http.NewRequest(http.MethodGet, "/default-adapter", nil)
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		w2 := httptest.NewRecorder()
		req2, _ := http.NewRequest(http.MethodGet, "/default-adapter", nil)
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	})
}
