package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

var (
	// ErrNilClient indicates a nil Redis client was provided.
	ErrNilClient = errors.New("nil redis client provided")
	// ErrNilStore indicates a nil store was provided.
	ErrNilStore = errors.New("nil rate limit store provided")
)

// Result represents the outcome of a rate limit check.
type Result struct {
	Allowed    bool          `json:"allowed"`
	Limit      int           `json:"limit"`
	Remaining  int           `json:"remaining"`
	Reset      time.Duration `json:"reset"`
	RetryAfter time.Duration `json:"retry_after"`
}

// Store defines the pluggable persistence contract for rate limit tracking.
type Store interface {
	// Allow evaluates whether a key is permitted under limit within window.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (*Result, error)
}

// -------------------------------------------------------------------------
// In-Memory Store with Sliding Window & LRU Eviction
// -------------------------------------------------------------------------

type memoryEntry struct {
	key        string
	timestamps []time.Time
	prev       *memoryEntry
	next       *memoryEntry
}

// MemoryStoreOption configures the in-memory rate limit store.
type MemoryStoreOption func(*MemoryStore)

// WithMaxEntries configures the maximum capacity of distinct tracked keys before LRU eviction.
func WithMaxEntries(maxEntries int) MemoryStoreOption {
	return func(m *MemoryStore) {
		if maxEntries > 0 {
			m.maxEntries = maxEntries
		}
	}
}

// MemoryStore provides in-memory sliding window rate limiting with thread-safe LRU eviction.
type MemoryStore struct {
	mu         sync.Mutex
	maxEntries int
	entries    map[string]*memoryEntry
	head       *memoryEntry // most recently used
	tail       *memoryEntry // least recently used
}

// NewMemoryStore initializes a new MemoryStore with LRU eviction.
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore {
	store := &MemoryStore{
		maxEntries: 10000,
		entries:    make(map[string]*memoryEntry),
	}
	// Setup sentinel nodes for doubly linked list
	store.head = &memoryEntry{}
	store.tail = &memoryEntry{}
	store.head.next = store.tail
	store.tail.prev = store.head

	for _, opt := range opts {
		opt(store)
	}
	return store
}

// Allow executes a sliding window check in-memory.
func (m *MemoryStore) Allow(_ context.Context, key string, limit int, window time.Duration) (*Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-window)

	entry, exists := m.entries[key]
	if !exists {
		// Enforce LRU eviction if at capacity
		if len(m.entries) >= m.maxEntries {
			m.evictLRU()
		}
		entry = &memoryEntry{
			key:        key,
			timestamps: make([]time.Time, 0, 8),
		}
		m.entries[key] = entry
		m.insertAtHead(entry)
	} else {
		m.moveToHead(entry)
	}

	// Prune timestamps older than the sliding window cutoff
	validIdx := 0
	for validIdx < len(entry.timestamps) && entry.timestamps[validIdx].Before(cutoff) {
		validIdx++
	}
	if validIdx > 0 {
		entry.timestamps = entry.timestamps[validIdx:]
	}

	currentCount := len(entry.timestamps)
	if currentCount < limit {
		entry.timestamps = append(entry.timestamps, now)
		remaining := limit - currentCount - 1
		resetDur := window
		if len(entry.timestamps) > 0 {
			oldest := entry.timestamps[0]
			resetDur = oldest.Add(window).Sub(now)
			if resetDur <= 0 {
				resetDur = window
			}
		}

		return &Result{
			Allowed:    true,
			Limit:      limit,
			Remaining:  remaining,
			Reset:      resetDur,
			RetryAfter: 0,
		}, nil
	}

	// Limit reached or exceeded
	oldest := entry.timestamps[0]
	retryAfter := oldest.Add(window).Sub(now)
	if retryAfter <= 0 {
		retryAfter = 1 * time.Millisecond
	}

	return &Result{
		Allowed:    false,
		Limit:      limit,
		Remaining:  0,
		Reset:      retryAfter,
		RetryAfter: retryAfter,
	}, nil
}

// Len returns the current number of tracked keys in memory.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// Clear removes all tracked keys.
func (m *MemoryStore) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]*memoryEntry)
	m.head.next = m.tail
	m.tail.prev = m.head
}

func (m *MemoryStore) insertAtHead(entry *memoryEntry) {
	entry.next = m.head.next
	entry.prev = m.head
	m.head.next.prev = entry
	m.head.next = entry
}

func (m *MemoryStore) removeFromList(entry *memoryEntry) {
	if entry.prev != nil {
		entry.prev.next = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	}
	entry.prev = nil
	entry.next = nil
}

func (m *MemoryStore) moveToHead(entry *memoryEntry) {
	m.removeFromList(entry)
	m.insertAtHead(entry)
}

func (m *MemoryStore) evictLRU() {
	lru := m.tail.prev
	if lru == nil || lru == m.head {
		return
	}
	m.removeFromList(lru)
	delete(m.entries, lru.key)
}

// -------------------------------------------------------------------------
// Redis Store with Sliding Window Lua Script
// -------------------------------------------------------------------------

// slidingWindowLuaScript executes atomic sliding window rate limiting in Redis.
// Obtains time directly from Redis server via redis.call('TIME') to eliminate
// multi-VM cluster clock skew and NTP drift.
// KEYS[1]: rate limit key
// ARGV[1]: window in microseconds
// ARGV[2]: limit
// ARGV[3]: key TTL in seconds
// ARGV[4]: unique member suffix
const slidingWindowLuaScript = `
local key = KEYS[1]
local window_micro = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])
local member_suffix = ARGV[4]

-- Obtain current time directly from Redis server
local now_tuple = redis.call('TIME')
local now = tonumber(now_tuple[1]) * 1000000 + tonumber(now_tuple[2])
local cutoff = now - window_micro
local member = tostring(now) .. '-' .. member_suffix

redis.call('ZREMRANGEBYSCORE', key, '-inf', cutoff)
local current_count = redis.call('ZCARD', key)

if current_count < limit then
    redis.call('ZADD', key, now, member)
    redis.call('EXPIRE', key, ttl)
    local remaining = limit - current_count - 1
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    local oldest_score = now
    if #oldest >= 2 then
        oldest_score = tonumber(oldest[2])
    end
    return {1, remaining, oldest_score, now}
else
    redis.call('EXPIRE', key, ttl)
    local oldest = redis.call('ZRANGE', key, 0, 0, 'WITHSCORES')
    local oldest_score = now
    if #oldest >= 2 then
        oldest_score = tonumber(oldest[2])
    end
    return {0, 0, oldest_score, now}
end
`

// RedisStore implements rate limiting on Redis using an atomic sliding window Lua script.
type RedisStore struct {
	client    goredis.Cmdable
	script    *goredis.Script
	memberSeq uint64
}

// NewRedisStore initializes a RedisStore using the provided Redis client.
func NewRedisStore(client goredis.Cmdable) *RedisStore {
	return &RedisStore{
		client: client,
		script: goredis.NewScript(slidingWindowLuaScript),
	}
}

// Allow executes an atomic sliding window check against Redis.
func (r *RedisStore) Allow(ctx context.Context, key string, limit int, window time.Duration) (*Result, error) {
	if r.client == nil {
		return nil, ErrNilClient
	}

	windowMicro := window.Microseconds()
	ttlSec := int(math.Ceil(window.Seconds())) + 2
	if ttlSec < 2 {
		ttlSec = 2
	}

	seq := atomic.AddUint64(&r.memberSeq, 1)
	memberSuffix := fmt.Sprintf("%d", seq)

	res, err := r.script.Run(ctx, r.client, []string{key}, windowMicro, limit, ttlSec, memberSuffix).Slice()
	if err != nil {
		return nil, err
	}

	if len(res) < 4 {
		return nil, fmt.Errorf("unexpected lua script response length: %d", len(res))
	}

	allowedInt, _ := res[0].(int64)
	remainingInt, _ := res[1].(int64)
	oldestScoreInt, _ := res[2].(int64)
	serverNowMicro, _ := res[3].(int64)

	allowed := allowedInt == 1
	remaining := int(remainingInt)

	serverNow := time.Unix(0, serverNowMicro*1000)
	oldestTime := time.Unix(0, oldestScoreInt*1000)
	windowEnd := oldestTime.Add(window)

	if allowed {
		reset := windowEnd.Sub(serverNow)
		if reset <= 0 {
			reset = window
		}
		return &Result{
			Allowed:    true,
			Limit:      limit,
			Remaining:  remaining,
			Reset:      reset,
			RetryAfter: 0,
		}, nil
	}

	retryAfter := windowEnd.Sub(serverNow)
	if retryAfter <= 0 {
		retryAfter = 1 * time.Millisecond
	}

	return &Result{
		Allowed:    false,
		Limit:      limit,
		Remaining:  0,
		Reset:      retryAfter,
		RetryAfter: retryAfter,
	}, nil
}

// -------------------------------------------------------------------------
// Fallback Store with Circuit-Breaking Cooldown
// -------------------------------------------------------------------------

// FallbackStore wraps a primary store (e.g. Redis) and falls back to a secondary
// store (e.g. MemoryStore) on error, entering a cooldown period before retrying primary.
type FallbackStore struct {
	primary       Store
	fallback      Store
	cooldown      time.Duration
	mu            sync.RWMutex
	cooldownUntil time.Time
}

// NewFallbackStore initializes a FallbackStore.
func NewFallbackStore(primary Store, fallback Store, cooldown time.Duration) *FallbackStore {
	if cooldown <= 0 {
		cooldown = 5 * time.Second
	}
	return &FallbackStore{
		primary:  primary,
		fallback: fallback,
		cooldown: cooldown,
	}
}

// Allow delegates to primary, falling back to local memory if primary errors or is in cooldown.
func (f *FallbackStore) Allow(ctx context.Context, key string, limit int, window time.Duration) (*Result, error) {
	now := time.Now()

	f.mu.RLock()
	inCooldown := now.Before(f.cooldownUntil)
	f.mu.RUnlock()

	if inCooldown {
		if f.fallback != nil {
			return f.fallback.Allow(ctx, key, limit, window)
		}
		return nil, errors.New("fallback store is nil during cooldown")
	}

	// Probe primary store
	res, err := f.primary.Allow(ctx, key, limit, window)
	if err == nil {
		// Successfully executed on primary: clear any old cooldown
		f.mu.Lock()
		f.cooldownUntil = time.Time{}
		f.mu.Unlock()
		return res, nil
	}

	// Primary failed: trigger cooldown timer
	f.mu.Lock()
	f.cooldownUntil = time.Now().Add(f.cooldown)
	f.mu.Unlock()

	// Automatically fall back to secondary store
	if f.fallback != nil {
		return f.fallback.Allow(ctx, key, limit, window)
	}

	return nil, err
}

// IsInCooldown returns whether the fallback store is currently in cooldown mode.
func (f *FallbackStore) IsInCooldown() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return time.Now().Before(f.cooldownUntil)
}

// CooldownUntil returns the timestamp when cooldown expires.
func (f *FallbackStore) CooldownUntil() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.cooldownUntil
}

// Primary returns the underlying primary store.
func (f *FallbackStore) Primary() Store {
	return f.primary
}

// Fallback returns the underlying fallback store.
func (f *FallbackStore) Fallback() Store {
	return f.fallback
}
