package telemetry

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// DefaultCacheTTL is the fallback cache duration for /health probe responses.
const DefaultCacheTTL = 2 * time.Second

// Config holds health check configuration and database clients.
type Config struct {
	CacheTTL time.Duration
	Env      string
	DB       *gorm.DB
	Redis    *goredis.Client
	Mongo    *mongo.Client
}

// Option configures HealthChecker.
type Option func(*HealthChecker)

// WithEnv sets the application environment name for diagnostic redaction decisions.
func WithEnv(env string) Option {
	return func(h *HealthChecker) {
		h.env = env
	}
}

// WithDB sets the GORM database client.
func WithDB(db *gorm.DB) Option {
	return func(h *HealthChecker) {
		h.db = db
	}
}

// WithRedis sets the Redis client.
func WithRedis(rdb *goredis.Client) Option {
	return func(h *HealthChecker) {
		h.redis = rdb
	}
}

// WithMongo sets the MongoDB client.
func WithMongo(client *mongo.Client) Option {
	return func(h *HealthChecker) {
		h.mongo = client
	}
}

// CheckStatus represents status of an individual dependency check.
type CheckStatus struct {
	Status string `json:"status"` // "UP" or "DOWN"
	Error  string `json:"error,omitempty"`
}

// SystemStats contains runtime and memory metrics.
type SystemStats struct {
	Goroutines      int    `json:"goroutines"`
	AllocBytes      uint64 `json:"alloc_bytes"`
	TotalAllocBytes uint64 `json:"total_alloc_bytes"`
	SysBytes        uint64 `json:"sys_bytes"`
	HeapAllocBytes  uint64 `json:"heap_alloc_bytes"`
	HeapSysBytes    uint64 `json:"heap_sys_bytes"`
	NumGC           uint32 `json:"num_gc"`
}

// HealthResponse represents the aggregate health response payload.
type HealthResponse struct {
	Status      string                 `json:"status"` // "UP" or "DOWN"
	Timestamp   time.Time              `json:"timestamp"`
	Services    map[string]CheckStatus `json:"services,omitempty"`
	SystemStats *SystemStats           `json:"system_stats,omitempty"`
}

// HealthChecker coordinates liveness, readiness, and deep dependency health checks.
type HealthChecker struct {
	cacheTTL time.Duration
	env      string
	db       *gorm.DB
	redis    *goredis.Client
	mongo    *mongo.Client

	draining atomic.Bool

	mu         sync.RWMutex
	cachedAt   time.Time
	cachedRes  HealthResponse
	cachedCode int

	sf singleflight.Group
}

type healthResult struct {
	code int
	res  HealthResponse
}

// NewHealthChecker constructs a new HealthChecker.
func NewHealthChecker(cfg Config, opts ...Option) *HealthChecker {
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}

	checker := &HealthChecker{
		cacheTTL: ttl,
		env:      cfg.Env,
		db:       cfg.DB,
		redis:    cfg.Redis,
		mongo:    cfg.Mongo,
	}

	for _, opt := range opts {
		opt(checker)
	}

	return checker
}

// SetDraining sets the atomic draining flag for graceful shutdown coordination.
func (h *HealthChecker) SetDraining(draining bool) {
	h.draining.Store(draining)
}

// IsDraining returns whether the server is currently in draining mode.
func (h *HealthChecker) IsDraining() bool {
	return h.draining.Load()
}

// RegisterRoutes registers /health, /health/ready, and /health/live endpoints on the router.
func (h *HealthChecker) RegisterRoutes(r gin.IRoutes) {
	r.GET("/health", h.HandleHealth)
	r.GET("/health/ready", h.HandleReady)
	r.GET("/health/live", h.HandleLive)
}

// HandleLive handles lightweight in-memory liveness probe requests.
func (h *HealthChecker) HandleLive(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "UP",
	})
}

// HandleReady handles readiness probe requests, returning 503 during graceful drain.
func (h *HealthChecker) HandleReady(c *gin.Context) {
	if h.draining.Load() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":  "DOWN",
			"message": "server is draining",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "UP",
	})
}

// HandleHealth handles aggregate deep health checks with response caching and singleflight coalescing.
func (h *HealthChecker) HandleHealth(c *gin.Context) {
	now := time.Now()

	// Fast path: check cache under read lock
	h.mu.RLock()
	if !h.cachedAt.IsZero() && now.Sub(h.cachedAt) < h.cacheTTL {
		res := h.cachedRes
		code := h.cachedCode
		h.mu.RUnlock()
		c.JSON(code, h.responseForContext(c, res))
		return
	}
	h.mu.RUnlock()

	// Slow path: coalesce concurrent probe operations via singleflight
	val, err, _ := h.sf.Do("health", func() (any, error) {
		// Double-check cache inside singleflight under read lock
		h.mu.RLock()
		innerNow := time.Now()
		if !h.cachedAt.IsZero() && innerNow.Sub(h.cachedAt) < h.cacheTTL {
			res := h.cachedRes
			code := h.cachedCode
			h.mu.RUnlock()
			return healthResult{code: code, res: res}, nil
		}
		h.mu.RUnlock()

		// Run dependency pings concurrently WITHOUT holding any lock
		services := make(map[string]CheckStatus)
		var (
			serviceMu sync.Mutex
			wg        sync.WaitGroup
			overallUp = true
		)

		// Check Postgres if configured
		if h.db != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status := CheckStatus{Status: "UP"}
				sqlDB, err := h.db.DB()
				if err != nil {
					status = CheckStatus{Status: "DOWN", Error: err.Error()}
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
					defer cancel()
					if err := sqlDB.PingContext(ctx); err != nil {
						status = CheckStatus{Status: "DOWN", Error: err.Error()}
					}
				}

				serviceMu.Lock()
				services["postgres"] = status
				if status.Status != "UP" {
					overallUp = false
				}
				serviceMu.Unlock()
			}()
		}

		// Check Redis if configured
		if h.redis != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status := CheckStatus{Status: "UP"}
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()
				if err := h.redis.Ping(ctx).Err(); err != nil {
					status = CheckStatus{Status: "DOWN", Error: err.Error()}
				}

				serviceMu.Lock()
				services["redis"] = status
				if status.Status != "UP" {
					overallUp = false
				}
				serviceMu.Unlock()
			}()
		}

		// Check MongoDB if configured
		if h.mongo != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				status := CheckStatus{Status: "UP"}
				ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
				defer cancel()
				if err := h.mongo.Ping(ctx, nil); err != nil {
					status = CheckStatus{Status: "DOWN", Error: err.Error()}
				}

				serviceMu.Lock()
				services["mongodb"] = status
				if status.Status != "UP" {
					overallUp = false
				}
				serviceMu.Unlock()
			}()
		}

		wg.Wait()

		// Read runtime memory metrics
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		stats := SystemStats{
			Goroutines:      runtime.NumGoroutine(),
			AllocBytes:      m.Alloc,
			TotalAllocBytes: m.TotalAlloc,
			SysBytes:        m.Sys,
			HeapAllocBytes:  m.HeapAlloc,
			HeapSysBytes:    m.HeapSys,
			NumGC:           m.NumGC,
		}

		statusCode := http.StatusOK
		statusStr := "UP"
		if !overallUp {
			statusCode = http.StatusServiceUnavailable
			statusStr = "DOWN"
		}

		res := HealthResponse{
			Status:      statusStr,
			Timestamp:   innerNow,
			Services:    services,
			SystemStats: &stats,
		}

		// Acquire write lock briefly to store the new cached result
		h.mu.Lock()
		h.cachedRes = res
		h.cachedCode = statusCode
		h.cachedAt = innerNow
		h.mu.Unlock()

		return healthResult{code: statusCode, res: res}, nil
	})

	if err != nil {
		if h.includeDiagnostics(c) {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "DOWN",
				"error":  err.Error(),
			})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "DOWN",
			})
		}
		return
	}

	result, ok := val.(healthResult)
	if !ok {
		if h.includeDiagnostics(c) {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "DOWN",
				"error":  "invalid health result",
			})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "DOWN",
			})
		}
		return
	}

	c.JSON(result.code, h.responseForContext(c, result.res))
}

func (h *HealthChecker) includeDiagnostics(c *gin.Context) bool {
	if strings.EqualFold(strings.TrimSpace(h.env), "development") {
		return true
	}
	if c != nil {
		if _, exists := c.Get("user"); exists {
			return true
		}
		if strings.TrimSpace(c.GetString("user_id")) != "" {
			return true
		}
	}
	return false
}

func (h *HealthChecker) responseForContext(c *gin.Context, res HealthResponse) HealthResponse {
	if h.includeDiagnostics(c) {
		return res
	}
	return redactHealthResponse(res)
}

func redactHealthResponse(res HealthResponse) HealthResponse {
	sanitized := HealthResponse{
		Status:    res.Status,
		Timestamp: res.Timestamp,
	}
	if len(res.Services) > 0 {
		sanitized.Services = make(map[string]CheckStatus, len(res.Services))
		for k, v := range res.Services {
			sanitized.Services[k] = CheckStatus{
				Status: v.Status,
			}
		}
	}
	return sanitized
}
