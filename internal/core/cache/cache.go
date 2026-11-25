package cache

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"

	"go_backend_boilerplate/internal/config"
)

var (
	// ErrNotFound indicates the item was not found in the source or is cached as negative entry.
	ErrNotFound = errors.New("cache: not found")
)

// Config configures multi-tier caching, TTL jitter, and distributed lock stampede defense.
type Config struct {
	TTLJitterRatio    float64       `json:"ttl_jitter_ratio"`
	DisableJitter     bool          `json:"disable_jitter"`
	DefaultSoftTTL    time.Duration `json:"default_soft_ttl"`
	DefaultHardTTL    time.Duration `json:"default_hard_ttl"`
	NegativeSoftTTL   time.Duration `json:"negative_soft_ttl"`
	NegativeHardTTL   time.Duration `json:"negative_hard_ttl"`
	LoaderLockTTL     time.Duration `json:"loader_lock_ttl"`
	LoaderWaitTimeout time.Duration `json:"loader_wait_timeout"`
	MaxMemoryEntries  int           `json:"max_memory_entries"`
}

// DefaultConfig returns safe production defaults for caching.
func DefaultConfig() Config {
	return Config{
		TTLJitterRatio:    0.15,
		DisableJitter:     false,
		DefaultSoftTTL:    60 * time.Second,
		DefaultHardTTL:    300 * time.Second,
		NegativeSoftTTL:   10 * time.Second,
		NegativeHardTTL:   300 * time.Second,
		LoaderLockTTL:     30 * time.Second,
		LoaderWaitTimeout: 5 * time.Second,
		MaxMemoryEntries:  10000,
	}
}

func applyConfigDefaults(cfg Config) Config {
	def := DefaultConfig()
	if cfg.DisableJitter || cfg.TTLJitterRatio < 0 {
		cfg.TTLJitterRatio = 0
	} else if cfg.TTLJitterRatio == 0 {
		cfg.TTLJitterRatio = def.TTLJitterRatio
	}
	if cfg.DefaultSoftTTL <= 0 {
		cfg.DefaultSoftTTL = def.DefaultSoftTTL
	}
	if cfg.DefaultHardTTL <= 0 {
		cfg.DefaultHardTTL = def.DefaultHardTTL
	}
	if cfg.NegativeSoftTTL <= 0 {
		cfg.NegativeSoftTTL = def.NegativeSoftTTL
	}
	if cfg.NegativeHardTTL <= 0 {
		cfg.NegativeHardTTL = def.NegativeHardTTL
	}
	if cfg.LoaderLockTTL <= 0 {
		cfg.LoaderLockTTL = def.LoaderLockTTL
	}
	if cfg.LoaderWaitTimeout <= 0 {
		cfg.LoaderWaitTimeout = def.LoaderWaitTimeout
	}
	if cfg.MaxMemoryEntries <= 0 {
		cfg.MaxMemoryEntries = def.MaxMemoryEntries
	}
	return cfg
}

// Options controls caching behavior per call.
type Options struct {
	SoftTTL         time.Duration
	HardTTL         time.Duration
	NegativeCache   bool
	NegativeSoftTTL time.Duration
	NegativeHardTTL time.Duration
	DisableLock     bool
}

// Envelope wraps cached values with timestamp metadata for SWR and negative caching.
type Envelope struct {
	Data       []byte    `json:"data"`
	CreatedAt  time.Time `json:"created_at"`
	SoftTTL    int64     `json:"soft_ttl_ns"`
	HardTTL    int64     `json:"hard_ttl_ns"`
	IsNegative bool      `json:"is_negative,omitempty"`
}

// IsFresh returns true if the entry is within its SoftTTL window.
func (e *Envelope) IsFresh(now time.Time) bool {
	return now.Sub(e.CreatedAt) <= time.Duration(e.SoftTTL)
}

// IsStale returns true if the entry is past SoftTTL but before HardTTL.
func (e *Envelope) IsStale(now time.Time) bool {
	age := now.Sub(e.CreatedAt)
	return age > time.Duration(e.SoftTTL) && age <= time.Duration(e.HardTTL)
}

// IsExpired returns true if the entry is older than its HardTTL window.
func (e *Envelope) IsExpired(now time.Time) bool {
	return now.Sub(e.CreatedAt) > time.Duration(e.HardTTL)
}

// Service defines the contract for multi-tier cache stores.
type Service interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, data []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	GetEnvelope(ctx context.Context, key string) (*Envelope, bool, error)
	SetEnvelope(ctx context.Context, key string, env *Envelope, hardTTL time.Duration) error
	Config() Config
	Redis() goredis.Cmdable
	Singleflight() *singleflight.Group
}

// Cache provides a stampede-proof multi-tier cache combining in-memory LRU,
// Redis distributed cache, singleflight request coalescing, and SWR.
type Cache struct {
	cfg   Config
	redis goredis.Cmdable
	sf    *singleflight.Group
	mem   *memoryLRU
}

// New initializes a multi-tier cache with the given config and optional Redis client.
func New(cfg Config, redisClient goredis.Cmdable) *Cache {
	resolvedCfg := applyConfigDefaults(cfg)
	return &Cache{
		cfg:   resolvedCfg,
		redis: redisClient,
		sf:    &singleflight.Group{},
		mem:   newMemoryLRU(resolvedCfg.MaxMemoryEntries),
	}
}

// NewMemoryCache initializes an in-memory only cache.
func NewMemoryCache(cfg Config) *Cache {
	return New(cfg, nil)
}

// NewFromAppConfig initializes a Cache from application config.CacheConfig.
func NewFromAppConfig(cfg config.CacheConfig, redisClient goredis.Cmdable) *Cache {
	c := DefaultConfig()
	if cfg.TTLJitterRatio > 0 {
		c.TTLJitterRatio = cfg.TTLJitterRatio
	}
	if cfg.DefaultSoftTTLSec > 0 {
		c.DefaultSoftTTL = time.Duration(cfg.DefaultSoftTTLSec) * time.Second
	}
	if cfg.DefaultHardTTLSec > 0 {
		c.DefaultHardTTL = time.Duration(cfg.DefaultHardTTLSec) * time.Second
	}
	if cfg.NegativeSoftTTLSec > 0 {
		c.NegativeSoftTTL = time.Duration(cfg.NegativeSoftTTLSec) * time.Second
	}
	return New(c, redisClient)
}

// Config returns the active cache configuration.
func (c *Cache) Config() Config {
	return c.cfg
}

// Redis returns the underlying Redis client (may be nil).
func (c *Cache) Redis() goredis.Cmdable {
	return c.redis
}

// Singleflight returns the singleflight group used for request coalescing.
func (c *Cache) Singleflight() *singleflight.Group {
	return c.sf
}

// Get retrieves raw bytes from cache (memory or Redis).
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	env, found, err := c.GetEnvelope(ctx, key)
	if err != nil || !found {
		return nil, found, err
	}
	if env.IsNegative {
		return nil, true, ErrNotFound
	}
	return env.Data, true, nil
}

// Set stores raw bytes in cache with jittered TTL.
func (c *Cache) Set(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	now := time.Now()
	jittered := ApplyJitter(ttl, c.cfg.TTLJitterRatio)
	env := &Envelope{
		Data:       data,
		CreatedAt:  now,
		SoftTTL:    int64(jittered),
		HardTTL:    int64(jittered),
		IsNegative: false,
	}
	return c.SetEnvelope(ctx, key, env, jittered)
}

// Delete removes the key from both local memory and Redis.
func (c *Cache) Delete(ctx context.Context, key string) error {
	if c.mem != nil {
		c.mem.Delete(key)
	}
	if c.redis != nil {
		return c.redis.Del(ctx, key).Err()
	}
	return nil
}

// GetEnvelope retrieves a cached envelope from L1 memory or L2 Redis.
func (c *Cache) GetEnvelope(ctx context.Context, key string) (*Envelope, bool, error) {
	now := time.Now()

	// 1. Check L1 Memory LRU
	if c.mem != nil {
		if env, found := c.mem.Get(key); found {
			if env.IsExpired(now) {
				c.mem.Delete(key)
			} else {
				return env, true, nil
			}
		}
	}

	// 2. Check L2 Redis
	if c.redis != nil {
		data, err := c.redis.Get(ctx, key).Bytes()
		if err == nil {
			var env Envelope
			if jsonErr := json.Unmarshal(data, &env); jsonErr == nil {
				if !env.IsExpired(now) {
					if c.mem != nil {
						c.mem.Set(key, &env)
					}
					return &env, true, nil
				}
			}
		} else if !errors.Is(err, goredis.Nil) {
			return nil, false, err
		}
	}

	return nil, false, nil
}

// SetEnvelope persists a cached envelope in L1 memory and L2 Redis.
func (c *Cache) SetEnvelope(ctx context.Context, key string, env *Envelope, hardTTL time.Duration) error {
	if c.mem != nil {
		c.mem.Set(key, env)
	}

	if c.redis != nil {
		data, err := json.Marshal(env)
		if err != nil {
			return err
		}
		if hardTTL <= 0 {
			hardTTL = time.Duration(env.HardTTL)
		}
		if err := c.redis.Set(ctx, key, data, hardTTL).Err(); err != nil {
			return err
		}
	}

	return nil
}

// ApplyJitter adds random variance in range [-ratio, +ratio] to the duration.
func ApplyJitter(dur time.Duration, ratio float64) time.Duration {
	if ratio <= 0 || dur <= 0 {
		return dur
	}
	if ratio > 1.0 {
		ratio = 1.0
	}
	delta := (cryptoFloat64()*2 - 1) * ratio
	jittered := time.Duration(float64(dur) * (1.0 + delta))
	if jittered <= 0 {
		return dur
	}
	return jittered
}

func cryptoFloat64() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	v := binary.BigEndian.Uint64(b[:]) >> 11
	return float64(v) / float64(uint64(1)<<53)
}

// GetOrSet retrieves an item from cache, or executes loader with singleflight deduplication,
// distributed loader lock, TTL jitter, SWR background revalidation, and negative caching.
func GetOrSet[T any](
	c Service,
	ctx context.Context,
	key string,
	loader func() (T, error),
	opts Options,
) (T, error) {
	var zero T

	// 1. Check Cache (L1 Memory & L2 Redis)
	env, found, err := c.GetEnvelope(ctx, key)
	if err == nil && found {
		now := time.Now()

		if env.IsFresh(now) {
			// Fresh Cache Hit
			if env.IsNegative {
				return zero, ErrNotFound
			}
			return unmarshalData[T](env.Data)
		}

		if env.IsStale(now) {
			// Stale Cache Hit: return stale data immediately and revalidate in background
			var staleVal T
			var staleErr error
			if env.IsNegative {
				staleErr = ErrNotFound
			} else {
				staleVal, staleErr = unmarshalData[T](env.Data)
			}

			if staleErr == nil || errors.Is(staleErr, ErrNotFound) {
				triggerBackgroundRefresh(c, key, loader, opts)
			}
			return staleVal, staleErr
		}
	}

	// 2. Cache Miss or Hard Expired: Coalesce via Singleflight
	res, sfErr, _ := c.Singleflight().Do(key, func() (any, error) {
		// Decouple context from singleflight initiator so that early client aborts
		// do not terminate shared loading for parallel callers.
		loaderTimeout := c.Config().LoaderLockTTL
		if loaderTimeout <= 0 {
			loaderTimeout = 30 * time.Second
		}
		opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loaderTimeout)
		defer cancel()

		// Double check cache in case a previous parallel request finished
		if doubleCheckEnv, found, doubleCheckErr := c.GetEnvelope(opCtx, key); doubleCheckErr == nil && found {
			now := time.Now()
			if !doubleCheckEnv.IsExpired(now) {
				if doubleCheckEnv.IsNegative {
					return nil, ErrNotFound
				}
				return unmarshalData[T](doubleCheckEnv.Data)
			}
		}

		// Distributed locking for multi-node deployments
		if c.Redis() != nil && !opts.DisableLock {
			lock, lockErr := TryAcquireLock(opCtx, c.Redis(), key, c.Config().LoaderLockTTL)
			if lockErr == nil {
				// Lock acquired: load data and release lock
				defer func() {
					_ = lock.Unlock(context.Background())
				}()

				loadedVal, loadErr := loader()
				if loadErr != nil {
					if errors.Is(loadErr, ErrNotFound) && (opts.NegativeCache || c.Config().NegativeSoftTTL > 0) {
						_ = setNegativeEntry(c, opCtx, key, opts)
					}
					return nil, loadErr
				}

				_ = setSuccessEntry[T](c, opCtx, key, loadedVal, opts)
				return loadedVal, nil
			} else if errors.Is(lockErr, ErrLockNotAcquired) {
				// Another node is loading: wait and poll Redis (respecting initiator ctx)
				polledEnv, found, pollErr := pollRedisForEnvelope(c, ctx, key)
				if pollErr != nil {
					// Honor context cancellation / deadline exceeded immediately
					return nil, pollErr
				}
				if found {
					if polledEnv.IsNegative {
						return nil, ErrNotFound
					}
					return unmarshalData[T](polledEnv.Data)
				}
				// Timeout or not found in Redis: fall through to execute loader locally
			}
			// Other Redis error or fallback: proceed to local load
		}

		// Memory-only or fallback loader execution
		loadedVal, loadErr := loader()
		if loadErr != nil {
			if errors.Is(loadErr, ErrNotFound) && (opts.NegativeCache || c.Config().NegativeSoftTTL > 0) {
				_ = setNegativeEntry(c, opCtx, key, opts)
			}
			return nil, loadErr
		}

		_ = setSuccessEntry[T](c, opCtx, key, loadedVal, opts)
		return loadedVal, nil
	})

	if sfErr != nil {
		return zero, sfErr
	}

	return res.(T), nil
}

func triggerBackgroundRefresh[T any](
	c Service,
	key string,
	loader func() (T, error),
	opts Options,
) {
	go func() {
		// Zero-Tolerance Panic Recovery shield for background revalidation
		defer func() {
			_ = recover()
		}()

		refreshKey := "refresh:" + key
		_, _, _ = c.Singleflight().Do(refreshKey, func() (any, error) {
			defer func() {
				_ = recover()
			}()

			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Harmonize lock key with foreground loaders so background revalidation
			// and foreground loaders synchronize on the same distributed lock
			if c.Redis() != nil && !opts.DisableLock {
				lock, err := TryAcquireLock(bgCtx, c.Redis(), key, c.Config().LoaderLockTTL)
				if err != nil {
					// Another node or foreground loader is already active
					return nil, nil
				}
				defer func() {
					_ = lock.Unlock(context.Background())
				}()
			}

			freshVal, err := loader()
			if err != nil {
				// Retain existing stale cache on error
				return nil, nil
			}

			_ = setSuccessEntry[T](c, bgCtx, key, freshVal, opts)
			return nil, nil
		})
	}()
}

func setSuccessEntry[T any](c Service, ctx context.Context, key string, val T, opts Options) error {
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}

	softTTL := opts.SoftTTL
	if softTTL <= 0 {
		softTTL = c.Config().DefaultSoftTTL
	}
	hardTTL := opts.HardTTL
	if hardTTL <= 0 {
		hardTTL = c.Config().DefaultHardTTL
	}
	if hardTTL < softTTL {
		hardTTL = softTTL
	}

	jitterRatio := c.Config().TTLJitterRatio
	jitteredSoft := ApplyJitter(softTTL, jitterRatio)
	jitteredHard := ApplyJitter(hardTTL, jitterRatio)
	if jitteredHard < jitteredSoft {
		jitteredHard = jitteredSoft
	}

	env := &Envelope{
		Data:       data,
		CreatedAt:  time.Now(),
		SoftTTL:    int64(jitteredSoft),
		HardTTL:    int64(jitteredHard),
		IsNegative: false,
	}

	return c.SetEnvelope(ctx, key, env, jitteredHard)
}

func setNegativeEntry(c Service, ctx context.Context, key string, opts Options) error {
	softTTL := opts.NegativeSoftTTL
	if softTTL <= 0 {
		softTTL = c.Config().NegativeSoftTTL
	}
	if softTTL <= 0 {
		softTTL = 10 * time.Second
	}

	hardTTL := opts.NegativeHardTTL
	if hardTTL <= 0 {
		hardTTL = c.Config().NegativeHardTTL
	}
	if hardTTL <= 0 {
		hardTTL = 60 * time.Second
	}
	if hardTTL < softTTL {
		hardTTL = softTTL
	}

	jitterRatio := c.Config().TTLJitterRatio
	jitteredSoft := ApplyJitter(softTTL, jitterRatio)
	jitteredHard := ApplyJitter(hardTTL, jitterRatio)

	env := &Envelope{
		Data:       nil,
		CreatedAt:  time.Now(),
		SoftTTL:    int64(jitteredSoft),
		HardTTL:    int64(jitteredHard),
		IsNegative: true,
	}

	return c.SetEnvelope(ctx, key, env, jitteredHard)
}

func pollRedisForEnvelope(c Service, ctx context.Context, key string) (*Envelope, bool, error) {
	// Immediate probe before ticker loop to avoid latency delay on fast lock release
	if env, found, err := c.GetEnvelope(ctx, key); err == nil && found {
		return env, true, nil
	}

	timeout := c.Config().LoaderWaitTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case now := <-ticker.C:
			env, found, err := c.GetEnvelope(ctx, key)
			if err == nil && found {
				return env, true, nil
			}
			if now.After(deadline) {
				return nil, false, nil
			}
		}
	}
}

func unmarshalData[T any](data []byte) (T, error) {
	var val T
	if len(data) == 0 {
		return val, nil
	}
	err := json.Unmarshal(data, &val)
	return val, err
}

// -------------------------------------------------------------------------
// Thread-Safe In-Memory LRU Cache
// -------------------------------------------------------------------------

type lruEntry struct {
	key   string
	value *Envelope
	prev  *lruEntry
	next  *lruEntry
}

type memoryLRU struct {
	mu       sync.RWMutex
	capacity int
	entries  map[string]*lruEntry
	head     *lruEntry
	tail     *lruEntry
}

func newMemoryLRU(capacity int) *memoryLRU {
	if capacity <= 0 {
		capacity = 10000
	}
	m := &memoryLRU{
		capacity: capacity,
		entries:  make(map[string]*lruEntry),
		head:     &lruEntry{},
		tail:     &lruEntry{},
	}
	m.head.next = m.tail
	m.tail.prev = m.head
	return m
}

func (m *memoryLRU) Get(key string) (*Envelope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.entries[key]
	if !exists {
		return nil, false
	}
	m.moveToHead(entry)
	return entry.value, true
}

func (m *memoryLRU) Set(key string, env *Envelope) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, exists := m.entries[key]; exists {
		entry.value = env
		m.moveToHead(entry)
		return
	}

	if len(m.entries) >= m.capacity {
		m.evictLRU()
	}

	entry := &lruEntry{
		key:   key,
		value: env,
	}
	m.entries[key] = entry
	m.insertAtHead(entry)
}

func (m *memoryLRU) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, exists := m.entries[key]; exists {
		m.removeEntry(entry)
		delete(m.entries, key)
	}
}

func (m *memoryLRU) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

func (m *memoryLRU) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]*lruEntry)
	m.head.next = m.tail
	m.tail.prev = m.head
}

func (m *memoryLRU) insertAtHead(entry *lruEntry) {
	entry.next = m.head.next
	entry.prev = m.head
	m.head.next.prev = entry
	m.head.next = entry
}

func (m *memoryLRU) removeEntry(entry *lruEntry) {
	if entry.prev != nil {
		entry.prev.next = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	}
	entry.prev = nil
	entry.next = nil
}

func (m *memoryLRU) moveToHead(entry *lruEntry) {
	m.removeEntry(entry)
	m.insertAtHead(entry)
}

func (m *memoryLRU) evictLRU() {
	lru := m.tail.prev
	if lru == nil || lru == m.head {
		return
	}
	m.removeEntry(lru)
	delete(m.entries, lru.key)
}
