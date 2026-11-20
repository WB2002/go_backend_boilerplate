package middleware

import (
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"go_backend_boilerplate/internal/core/ratelimit"
)

var (
	defaultLimiterMu sync.RWMutex
	defaultLimiter   = ratelimit.New(ratelimit.NewMemoryStore(), ratelimit.Config{
		DefaultLimit:     100,
		DefaultWindow:    60 * time.Second,
		FallbackCooldown: 5 * time.Second,
	})
)

// SetDefaultLimiter configures the package-level default rate limiter used by RateLimit in a thread-safe manner.
func SetDefaultLimiter(limiter *ratelimit.Limiter) {
	if limiter == nil {
		return
	}
	defaultLimiterMu.Lock()
	defer defaultLimiterMu.Unlock()
	defaultLimiter = limiter
}

// DefaultLimiter returns the currently active package-level default rate limiter safely under read lock.
func DefaultLimiter() *ratelimit.Limiter {
	defaultLimiterMu.RLock()
	defer defaultLimiterMu.RUnlock()
	return defaultLimiter
}

// RateLimitWithLimiter returns a Gin middleware adapter enforcing rate limiting using the provided limiter.
// This is the primary explicit dependency-injection middleware function.
func RateLimitWithLimiter(limiter *ratelimit.Limiter, identifier ratelimit.IdentifierType, limit int, window time.Duration) gin.HandlerFunc {
	if limiter != nil {
		return limiter.Middleware(identifier, limit, window)
	}
	return DefaultLimiter().Middleware(identifier, limit, window)
}

// RateLimit returns a Gin middleware adapter enforcing rate limiting using the package-level default limiter.
func RateLimit(identifier ratelimit.IdentifierType, limit int, window time.Duration) gin.HandlerFunc {
	return RateLimitWithLimiter(nil, identifier, limit, window)
}
