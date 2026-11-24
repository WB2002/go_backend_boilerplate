package overload

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"go_backend_boilerplate/internal/config"
)

// Priority represents the classification level of an inbound request.
type Priority string

// Priority classification levels.
const (
	// PriorityLow is for non-critical background jobs, reports, or sync tasks.
	PriorityLow Priority = "low"
	// PriorityNormal is the default priority for standard API interactions.
	PriorityNormal Priority = "normal"
	// PriorityHigh is for elevated, interactive customer workflows.
	PriorityHigh Priority = "high"
	// PriorityCritical is reserved for system health, auth, and critical billing/checkouts.
	PriorityCritical Priority = "critical"
)

// ParsePriority converts an arbitrary string to a Priority tier, defaulting to PriorityNormal.
func ParsePriority(s string) Priority {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(PriorityLow):
		return PriorityLow
	case string(PriorityNormal):
		return PriorityNormal
	case string(PriorityHigh):
		return PriorityHigh
	case string(PriorityCritical):
		return PriorityCritical
	default:
		return PriorityNormal
	}
}

// Config defines the inbound overload protection and concurrency bulkhead parameters.
type Config struct {
	MaxInFlight             int           `json:"max_in_flight"`
	PrioritySheddingEnabled bool          `json:"priority_shedding_enabled"`
	RetryAfterSec           int           `json:"retry_after_sec"`
	DefaultPriority         Priority      `json:"default_priority,omitempty"`
	MaxHeapBytes            uint64        `json:"max_heap_bytes,omitempty"`
	MaxGCPause              time.Duration `json:"max_gc_pause,omitempty"`
	LoadLevelMaxWait        time.Duration `json:"load_level_max_wait,omitempty"`
	LoadLevelQueueSize      int           `json:"load_level_queue_size,omitempty"`
	MonitorInterval         time.Duration `json:"monitor_interval,omitempty"`
}

// Stats provides runtime snapshots of overload protection metrics.
type Stats struct {
	InFlight         int64  `json:"in_flight"`
	MaxInFlight      int    `json:"max_in_flight"`
	TotalShed        int64  `json:"total_shed"`
	TotalAccepted    int64  `json:"total_accepted"`
	CurrentHeapAlloc uint64 `json:"current_heap_alloc_bytes"`
	LastGCPauseNs    uint64 `json:"last_gc_pause_ns"`
	UnderPressure    bool   `json:"under_pressure"`
}

// Manager tracks active in-flight requests, monitors GC/memory pressure, and enforces priority load shedding.
type Manager struct {
	config Config

	inFlight      int64
	totalShed     int64
	totalAccepted int64

	currentHeapAlloc uint64
	lastGCPauseNs    uint64
	underPressure    int32 // 1 if pressured, 0 otherwise

	waitingCount int64

	mu              sync.RWMutex
	routePriorities map[string]Priority
	nextListenerID  uint64
	listeners       map[uint64]chan struct{}

	stopCh chan struct{}
}

// NewManager initializes and returns an OverloadManager.
func NewManager(cfg Config) *Manager {
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 1000
	}
	if cfg.RetryAfterSec <= 0 {
		cfg.RetryAfterSec = 5
	}
	if cfg.MonitorInterval <= 0 {
		cfg.MonitorInterval = 500 * time.Millisecond
	}
	if cfg.DefaultPriority == "" {
		cfg.DefaultPriority = PriorityNormal
	}

	mgr := &Manager{
		config:          cfg,
		routePriorities: make(map[string]Priority),
		listeners:       make(map[uint64]chan struct{}),
		stopCh:          make(chan struct{}),
	}

	// Sample once on startup
	mgr.samplePressure()

	if cfg.MaxHeapBytes > 0 || cfg.MaxGCPause > 0 {
		go mgr.startMonitor()
	}

	return mgr
}

// NewManagerFromConfig creates an OverloadManager initialized from config.OverloadConfig.
func NewManagerFromConfig(cfg config.OverloadConfig) *Manager {
	return NewManager(Config{
		MaxInFlight:             cfg.MaxInFlight,
		PrioritySheddingEnabled: cfg.PrioritySheddingEnabled,
		RetryAfterSec:           cfg.RetryAfterSec,
		LoadLevelQueueSize:      cfg.InboundLoadLevelMaxQueue,
		LoadLevelMaxWait:        time.Duration(cfg.InboundLoadLevelMaxWaitMs) * time.Millisecond,
	})
}

// RegisterRoutePriority maps a specific route path to a priority tier.
func (m *Manager) RegisterRoutePriority(path string, p Priority) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routePriorities[path] = p
}

func (m *Manager) startMonitor() {
	ticker := time.NewTicker(m.config.MonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.samplePressure()
		case <-m.stopCh:
			return
		}
	}
}

// SamplePressure explicitly reads memory and GC stats to update pressure state.
func (m *Manager) SamplePressure() {
	m.samplePressure()
}

func (m *Manager) samplePressure() {
	if m.config.MaxHeapBytes == 0 && m.config.MaxGCPause == 0 {
		return
	}

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	atomic.StoreUint64(&m.currentHeapAlloc, memStats.HeapAlloc)

	lastPause := uint64(0)
	if memStats.NumGC > 0 {
		lastPause = memStats.PauseNs[(memStats.NumGC+255)%256]
	}
	atomic.StoreUint64(&m.lastGCPauseNs, lastPause)

	pressured := (m.config.MaxHeapBytes > 0 && memStats.HeapAlloc > m.config.MaxHeapBytes) ||
		(m.config.MaxGCPause > 0 && lastPause > uint64(m.config.MaxGCPause.Nanoseconds()))

	if pressured {
		atomic.StoreInt32(&m.underPressure, 1)
	} else {
		atomic.StoreInt32(&m.underPressure, 0)
	}
}

// Acquire increments in-flight active request count unconditionally (used for tests and manual token acquisition).
func (m *Manager) Acquire() bool {
	atomic.AddInt64(&m.inFlight, 1)
	atomic.AddInt64(&m.totalAccepted, 1)
	return true
}

// TryAcquire attempts to atomically admit an in-flight request for priority p using a CAS loop.
// Returns true if admitted, false if shedding threshold is met or system is under pressure.
func (m *Manager) TryAcquire(p Priority) bool {
	if m.IsUnderPressure() && p != PriorityCritical {
		return false
	}

	max := float64(m.config.MaxInFlight)

	for {
		curr := atomic.LoadInt64(&m.inFlight)

		if !m.config.PrioritySheddingEnabled {
			if float64(curr) >= max {
				return false
			}
		} else {
			switch p {
			case PriorityLow:
				if float64(curr) >= max*0.75 {
					return false
				}
			case PriorityNormal:
				if float64(curr) >= max*0.90 {
					return false
				}
			case PriorityHigh:
				if float64(curr) >= max*1.00 {
					return false
				}
			case PriorityCritical:
				reserveCap := max * 1.25
				if reserveCap <= max {
					reserveCap = max + 1
				}
				if float64(curr) >= reserveCap {
					return false
				}
			default:
				if float64(curr) >= max*0.90 {
					return false
				}
			}
		}

		if atomic.CompareAndSwapInt64(&m.inFlight, curr, curr+1) {
			atomic.AddInt64(&m.totalAccepted, 1)
			return true
		}
	}
}

// Release decrements in-flight active request count and notifies any waiting load-leveling requests.
func (m *Manager) Release() {
	for {
		curr := atomic.LoadInt64(&m.inFlight)
		if curr <= 0 {
			break
		}
		if atomic.CompareAndSwapInt64(&m.inFlight, curr, curr-1) {
			m.broadcastAvailable()
			break
		}
	}
}

func (m *Manager) broadcastAvailable() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ch := range m.listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// InFlight returns the current number of active in-flight requests.
func (m *Manager) InFlight() int64 {
	return atomic.LoadInt64(&m.inFlight)
}

// IsUnderPressure returns true if memory or GC pause exceeds configured thresholds.
// It is strictly lock-free and non-blocking, reading the atomically cached monitor state.
func (m *Manager) IsUnderPressure() bool {
	return atomic.LoadInt32(&m.underPressure) == 1
}

// ShouldShed evaluates whether a request of the specified priority should be shed given the current in-flight count.
func (m *Manager) ShouldShed(p Priority) bool {
	if m.IsUnderPressure() && p != PriorityCritical {
		return true
	}

	curr := atomic.LoadInt64(&m.inFlight)
	max := float64(m.config.MaxInFlight)

	if !m.config.PrioritySheddingEnabled {
		return float64(curr) >= max
	}

	switch p {
	case PriorityLow:
		return float64(curr) >= max*0.75
	case PriorityNormal:
		return float64(curr) >= max*0.90
	case PriorityHigh:
		return float64(curr) >= max*1.00
	case PriorityCritical:
		reserveCap := max * 1.25
		if reserveCap <= max {
			reserveCap = max + 1
		}
		return float64(curr) >= reserveCap
	default:
		return float64(curr) >= max*0.90
	}
}

// ExtractPriority extracts the Priority using the provided Manager (which may be nil).
func ExtractPriority(c *gin.Context, m *Manager) Priority {
	return m.ExtractPriority(c)
}

// ExtractPriority extracts the Priority from Gin context, route registration, or default.
func (m *Manager) ExtractPriority(c *gin.Context) Priority {
	if c == nil {
		if m != nil && m.config.DefaultPriority != "" {
			return m.config.DefaultPriority
		}
		return PriorityNormal
	}

	// 1. Gin context key: c.Get("api_priority") or c.Get("priority")
	for _, key := range []string{"api_priority", "priority"} {
		if val, exists := c.Get(key); exists {
			switch v := val.(type) {
			case Priority:
				return v
			case string:
				return ParsePriority(v)
			}
		}
	}

	// 2. Registered route path mapping
	if m != nil {
		m.mu.RLock()
		if p, ok := m.routePriorities[c.FullPath()]; ok {
			m.mu.RUnlock()
			return p
		}
		if c.Request != nil && c.Request.URL != nil {
			if p, ok := m.routePriorities[c.Request.URL.Path]; ok {
				m.mu.RUnlock()
				return p
			}
		}
		m.mu.RUnlock()
	}

	// 3. Exact path segment check (e.g. "/low", "/critical", "/high") without loose substring matching
	if c.Request != nil && c.Request.URL != nil {
		trimmed := strings.Trim(strings.ToLower(c.Request.URL.Path), "/")
		switch trimmed {
		case "low":
			return PriorityLow
		case "critical":
			return PriorityCritical
		case "high":
			return PriorityHigh
		}
	}

	// 4. Default priority
	if m != nil && m.config.DefaultPriority != "" {
		return m.config.DefaultPriority
	}
	return PriorityNormal
}

// WithPriority creates a route-level Gin middleware annotating the priority for the request.
func WithPriority(priority Priority) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("api_priority", priority)
		c.Set("priority", priority)
		c.Next()
	}
}

// Middleware returns a Gin middleware enforcing concurrency bulkheads and priority shedding.
func (m *Manager) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		priority := m.ExtractPriority(c)

		admitted := m.TryAcquire(priority)
		if !admitted && m.config.LoadLevelMaxWait > 0 {
			admitted = m.waitForSlot(c.Request.Context(), priority)
		}

		if !admitted {
			m.reject(c)
			return
		}

		defer m.Release()
		c.Next()
	}
}

func (m *Manager) reject(c *gin.Context) {
	atomic.AddInt64(&m.totalShed, 1)
	retrySec := m.config.RetryAfterSec
	if retrySec <= 0 {
		retrySec = 5
	}
	c.Header("Retry-After", strconv.Itoa(retrySec))
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
		"error": "service unavailable: system overloaded",
	})
}

func (m *Manager) waitForSlot(ctx context.Context, p Priority) bool {
	if m.config.LoadLevelMaxWait <= 0 {
		return false
	}
	if m.config.LoadLevelQueueSize > 0 {
		waiting := atomic.LoadInt64(&m.waitingCount)
		if waiting >= int64(m.config.LoadLevelQueueSize) {
			return false
		}
	}

	atomic.AddInt64(&m.waitingCount, 1)
	defer atomic.AddInt64(&m.waitingCount, -1)

	timer := time.NewTimer(m.config.LoadLevelMaxWait)
	defer timer.Stop()

	wakeChan := make(chan struct{}, 1)
	m.mu.Lock()
	id := m.nextListenerID
	m.nextListenerID++
	m.listeners[id] = wakeChan
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.listeners, id)
		m.mu.Unlock()
	}()

	for {
		// Contend atomically via TryAcquire
		if m.TryAcquire(p) {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			// Final try upon timeout
			return m.TryAcquire(p)
		case <-wakeChan:
			// Woken up by Release; loop around and contend via TryAcquire
			if m.TryAcquire(p) {
				return true
			}
		case <-m.stopCh:
			return false
		}
	}
}

// Stop cleanly terminates background monitoring routines.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.stopCh:
		// already closed
	default:
		close(m.stopCh)
	}
}

// Close terminates background routines (alias for Stop).
func (m *Manager) Close() error {
	m.Stop()
	return nil
}

// Stats returns a snapshot of current overload metrics.
func (m *Manager) Stats() Stats {
	return Stats{
		InFlight:         atomic.LoadInt64(&m.inFlight),
		MaxInFlight:      m.config.MaxInFlight,
		TotalShed:        atomic.LoadInt64(&m.totalShed),
		TotalAccepted:    atomic.LoadInt64(&m.totalAccepted),
		CurrentHeapAlloc: atomic.LoadUint64(&m.currentHeapAlloc),
		LastGCPauseNs:    atomic.LoadUint64(&m.lastGCPauseNs),
		UnderPressure:    m.IsUnderPressure(),
	}
}
