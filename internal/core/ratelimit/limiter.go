package ratelimit

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/auth"
)

// IdentifierType classifies the criteria used to differentiate rate-limited clients.
type IdentifierType string

const (
	// IdentifierIP identifies clients by their IP address.
	IdentifierIP IdentifierType = "ip"
	// IdentifierUserID identifies clients by their authenticated user identifier.
	IdentifierUserID IdentifierType = "user_id"
	// IdentifierDeviceFingerprint identifies clients by the X-Device-Fingerprint HTTP header.
	IdentifierDeviceFingerprint IdentifierType = "device_fingerprint"
	// IdentifierGuestSession identifies clients by the guest_session HTTP cookie.
	IdentifierGuestSession IdentifierType = "guest_session"
)

// Config defines the rate limiting policy defaults and fallback behavior.
type Config struct {
	DefaultLimit     int           `json:"default_limit"`
	DefaultWindow    time.Duration `json:"default_window"`
	FallbackCooldown time.Duration `json:"fallback_cooldown"`
}

// MiddlewareConfig provides optional route-level overrides for rate limiting middleware.
type MiddlewareConfig struct {
	RouteScoped      bool
	KeyPrefix        string
	CustomIdentifier func(c *gin.Context) string
}

// MiddlewareOption applies customizations to the rate limiting middleware.
type MiddlewareOption func(*MiddlewareConfig)

// WithRouteScope incorporates the matched route path into the rate limit key.
func WithRouteScope(scoped bool) MiddlewareOption {
	return func(c *MiddlewareConfig) {
		c.RouteScoped = scoped
	}
}

// WithKeyPrefix sets a custom prefix for the rate limit key.
func WithKeyPrefix(prefix string) MiddlewareOption {
	return func(c *MiddlewareConfig) {
		c.KeyPrefix = prefix
	}
}

// WithCustomIdentifier provides a custom identifier extraction function.
func WithCustomIdentifier(fn func(c *gin.Context) string) MiddlewareOption {
	return func(c *MiddlewareConfig) {
		c.CustomIdentifier = fn
	}
}

// Limiter coordinates rate limiting decisions, storage persistence, and Gin middleware generation.
type Limiter struct {
	store  Store
	config Config
}

// New initializes a RateLimiter with the specified store and configuration.
// If the provided store is not an in-memory store and not already a FallbackStore,
// it automatically wraps it in a FallbackStore with an in-memory fallback.
func New(store Store, cfg Config) *Limiter {
	if cfg.DefaultLimit <= 0 {
		cfg.DefaultLimit = 100
	}
	if cfg.DefaultWindow <= 0 {
		cfg.DefaultWindow = 60 * time.Second
	}
	if cfg.FallbackCooldown <= 0 {
		cfg.FallbackCooldown = 5 * time.Second
	}

	activeStore := store
	if activeStore == nil {
		activeStore = NewMemoryStore()
	} else {
		// Wrap with FallbackStore if not already in-memory or FallbackStore
		if _, isMem := activeStore.(*MemoryStore); !isMem {
			if _, isFb := activeStore.(*FallbackStore); !isFb {
				activeStore = NewFallbackStore(activeStore, NewMemoryStore(), cfg.FallbackCooldown)
			}
		}
	}

	return &Limiter{
		store:  activeStore,
		config: cfg,
	}
}

// NewWithFallback explicitly configures a limiter with primary and secondary fallback stores.
func NewWithFallback(primary Store, fallback Store, cfg Config) *Limiter {
	if cfg.FallbackCooldown <= 0 {
		cfg.FallbackCooldown = 5 * time.Second
	}
	fbStore := NewFallbackStore(primary, fallback, cfg.FallbackCooldown)
	return &Limiter{
		store:  fbStore,
		config: cfg,
	}
}

// NewFromConfig builds a Limiter from the application configuration.
func NewFromConfig(cfg config.RateLimitConfig, redisClient goredis.Cmdable) *Limiter {
	cooldown := time.Duration(cfg.FallbackCooldownMs) * time.Millisecond
	if cooldown <= 0 {
		cooldown = 5 * time.Second
	}
	window := time.Duration(cfg.DefaultWindowSec) * time.Second
	if window <= 0 {
		window = 60 * time.Second
	}
	limit := cfg.DefaultLimit
	if limit <= 0 {
		limit = 100
	}

	limiterCfg := Config{
		DefaultLimit:     limit,
		DefaultWindow:    window,
		FallbackCooldown: cooldown,
	}

	memStore := NewMemoryStore()
	if redisClient != nil {
		redisStore := NewRedisStore(redisClient)
		return NewWithFallback(redisStore, memStore, limiterCfg)
	}

	return New(memStore, limiterCfg)
}

// Store returns the underlying storage implementation.
func (l *Limiter) Store() Store {
	return l.store
}

// Allow directly evaluates a rate limit check against the store.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (*Result, error) {
	if limit <= 0 {
		limit = l.config.DefaultLimit
	}
	if window <= 0 {
		window = l.config.DefaultWindow
	}
	return l.store.Allow(ctx, key, limit, window)
}

// ExtractIdentifier extracts the key value from the Gin request context based on identifier type.
func ExtractIdentifier(c *gin.Context, idType IdentifierType) string {
	switch idType {
	case IdentifierIP:
		ip := c.ClientIP()
		if ip == "" {
			ip = "unknown"
		}
		return ip

	case IdentifierUserID:
		if uid := c.GetString("user_id"); uid != "" {
			return uid
		}
		if val, exists := c.Get("user"); exists {
			if claims, ok := val.(*auth.Claims); ok && claims.UserID != "" {
				return claims.UserID
			}
			if uidGetter, ok := val.(interface{ GetUserID() string }); ok {
				if uid := uidGetter.GetUserID(); uid != "" {
					return uid
				}
			}
		}
		// Unauthenticated user falls back to client IP with prefix
		ip := c.ClientIP()
		if ip == "" {
			ip = "unknown"
		}
		return "anon:" + ip

	case IdentifierDeviceFingerprint:
		if fp := c.GetHeader("X-Device-Fingerprint"); fp != "" {
			return fp
		}
		ip := c.ClientIP()
		if ip == "" {
			ip = "unknown"
		}
		return "fp_fallback:" + ip

	case IdentifierGuestSession:
		if cookie, err := c.Cookie("guest_session"); err == nil && cookie != "" {
			return cookie
		}
		ip := c.ClientIP()
		if ip == "" {
			ip = "unknown"
		}
		return "guest_fallback:" + ip

	default:
		ip := c.ClientIP()
		if ip == "" {
			ip = "unknown"
		}
		return ip
	}
}

// Middleware returns a Gin middleware that enforces rate limiting for the specified identifier.
func (l *Limiter) Middleware(identifier IdentifierType, limit int, window time.Duration, opts ...MiddlewareOption) gin.HandlerFunc {
	if limit <= 0 {
		limit = l.config.DefaultLimit
	}
	if window <= 0 {
		window = l.config.DefaultWindow
	}

	cfg := MiddlewareConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(c *gin.Context) {
		var idVal string
		if cfg.CustomIdentifier != nil {
			idVal = cfg.CustomIdentifier(c)
		} else {
			idVal = ExtractIdentifier(c, identifier)
		}

		prefix := "ratelimit"
		if cfg.KeyPrefix != "" {
			prefix = cfg.KeyPrefix
		}

		key := fmt.Sprintf("%s:%s:%s", prefix, identifier, idVal)
		if cfg.RouteScoped {
			route := c.FullPath()
			if route == "" {
				route = strings.Trim(c.Request.URL.Path, "/")
				if route == "" {
					route = "root"
				}
			}
			key = fmt.Sprintf("%s:%s:%s:%s", prefix, route, identifier, idVal)
		}

		res, err := l.store.Allow(c.Request.Context(), key, limit, window)
		if err != nil {
			// Fail open on unexpected storage infrastructure errors to preserve availability
			c.Next()
			return
		}

		now := time.Now()
		if res.Allowed {
			resetUnix := now.Add(res.Reset).Unix()
			c.Header("X-RateLimit-Limit", strconv.Itoa(limit))
			c.Header("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
			c.Header("X-RateLimit-Reset", strconv.FormatInt(resetUnix, 10))
			c.Next()
			return
		}

		// Rate limit exceeded
		retrySec := int(math.Ceil(res.RetryAfter.Seconds()))
		if retrySec <= 0 {
			retrySec = 1
		}
		resetUnix := now.Add(res.RetryAfter).Unix()
		if resetUnix <= now.Unix() {
			resetUnix = now.Unix() + int64(retrySec)
		}

		c.Header("X-RateLimit-Limit", strconv.Itoa(limit))
		c.Header("X-RateLimit-Remaining", "0")
		c.Header("X-RateLimit-Reset", strconv.FormatInt(resetUnix, 10))
		c.Header("Retry-After", strconv.Itoa(retrySec))

		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": "rate limit exceeded",
		})
	}
}
