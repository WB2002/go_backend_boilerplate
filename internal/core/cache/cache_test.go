package cache_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/cache"
)

func TestCacheSingleFlightDeduplication(t *testing.T) {
	c := cache.NewMemoryCache(cache.Config{
		TTLJitterRatio: 0.15,
	})

	var loaderCalls int32
	loader := func() (string, error) {
		atomic.AddInt32(&loaderCalls, 1)
		time.Sleep(50 * time.Millisecond)
		return "cached-data", nil
	}

	var wg sync.WaitGroup
	results := make([]string, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val, err := cache.GetOrSet(c, context.Background(), "test-key", loader, cache.Options{
				SoftTTL: 1 * time.Minute,
				HardTTL: 5 * time.Minute,
			})
			if err == nil {
				results[idx] = val
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&loaderCalls), "Loader should only be called once via singleflight")
	for _, res := range results {
		assert.Equal(t, "cached-data", res)
	}
}

func TestCacheStaleWhileRevalidate(t *testing.T) {
	c := cache.NewMemoryCache(cache.Config{
		DisableJitter: true,
	})

	var loaderCalls int32
	var loaderDone int32
	loaderVal := "v1"

	loader := func() (string, error) {
		atomic.AddInt32(&loaderCalls, 1)
		// Small delay to simulate database load
		time.Sleep(30 * time.Millisecond)
		res := loaderVal
		atomic.AddInt32(&loaderDone, 1)
		return res, nil
	}

	ctx := context.Background()

	// Initial call: populates cache with SoftTTL=40ms, HardTTL=300ms
	val1, err := cache.GetOrSet(c, ctx, "swr-key", loader, cache.Options{
		SoftTTL: 40 * time.Millisecond,
		HardTTL: 300 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, "v1", val1)
	assert.Equal(t, int32(1), atomic.LoadInt32(&loaderCalls))

	// Wait until SoftTTL expires (becomes stale), but before HardTTL
	time.Sleep(50 * time.Millisecond)

	// Change origin value to v2
	loaderVal = "v2"

	// Second call: entry is STALE.
	// SWR contract: MUST return stale value "v1" immediately without waiting 30ms for loader!
	start := time.Now()
	val2, err := cache.GetOrSet(c, ctx, "swr-key", loader, cache.Options{
		SoftTTL: 40 * time.Millisecond,
		HardTTL: 300 * time.Millisecond,
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "v1", val2, "Must return stale value immediately to caller")
	assert.Less(t, elapsed, 20*time.Millisecond, "SWR hit must not block caller waiting on loader")

	// Wait for background refresh goroutine to complete
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&loaderDone) == 2
	}, 300*time.Millisecond, 10*time.Millisecond, "Background refresh must be completed")

	// Allow envelope to be stored
	time.Sleep(20 * time.Millisecond)

	// Third call: background refresh completed, should return "v2"
	val3, err := cache.GetOrSet(c, ctx, "swr-key", loader, cache.Options{
		SoftTTL: 40 * time.Millisecond,
		HardTTL: 300 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, "v2", val3, "Subsequent call must return refreshed value")
}

func TestCacheTTLJitterVariance(t *testing.T) {
	baseTTL := 100 * time.Second
	jitterRatio := 0.15

	minAllowed := time.Duration(float64(baseTTL) * (1.0 - jitterRatio))
	maxAllowed := time.Duration(float64(baseTTL) * (1.0 + jitterRatio))

	var sum float64
	samples := 100
	seenValues := make(map[time.Duration]bool)

	for i := 0; i < samples; i++ {
		jittered := cache.ApplyJitter(baseTTL, jitterRatio)
		assert.GreaterOrEqual(t, jittered, minAllowed, "Jittered TTL must be >= min boundary")
		assert.LessOrEqual(t, jittered, maxAllowed, "Jittered TTL must be <= max boundary")
		sum += float64(jittered)
		seenValues[jittered] = true
	}

	// Verify non-zero variance (not all values identical)
	assert.Greater(t, len(seenValues), 10, "TTL Jitter must produce variable distribution")

	// Verify mean is close to baseTTL (within 5%)
	mean := time.Duration(sum / float64(samples))
	assert.InDelta(t, float64(baseTTL), float64(mean), float64(5*time.Second), "Mean jittered TTL must center around base TTL")

	// Edge cases: ratio <= 0 or duration <= 0
	assert.Equal(t, baseTTL, cache.ApplyJitter(baseTTL, 0))
	assert.Equal(t, time.Duration(0), cache.ApplyJitter(0, 0.15))
}

func TestCacheNegativeCaching(t *testing.T) {
	c := cache.NewMemoryCache(cache.Config{
		DisableJitter:   true,
		NegativeSoftTTL: 50 * time.Millisecond,
		NegativeHardTTL: 120 * time.Millisecond,
	})

	var loaderCalls int32
	loader := func() (string, error) {
		atomic.AddInt32(&loaderCalls, 1)
		return "", cache.ErrNotFound
	}

	ctx := context.Background()

	// 1. Initial call: loader returns ErrNotFound
	val, err := cache.GetOrSet(c, ctx, "missing-entity-123", loader, cache.Options{
		NegativeCache:   true,
		NegativeSoftTTL: 50 * time.Millisecond,
		NegativeHardTTL: 120 * time.Millisecond,
	})
	assert.ErrorIs(t, err, cache.ErrNotFound)
	assert.Empty(t, val)
	assert.Equal(t, int32(1), atomic.LoadInt32(&loaderCalls))

	// 2. Second call immediately: should return cached ErrNotFound without calling loader
	val2, err2 := cache.GetOrSet(c, ctx, "missing-entity-123", loader, cache.Options{
		NegativeCache:   true,
		NegativeSoftTTL: 50 * time.Millisecond,
		NegativeHardTTL: 120 * time.Millisecond,
	})
	assert.ErrorIs(t, err2, cache.ErrNotFound)
	assert.Empty(t, val2)
	assert.Equal(t, int32(1), atomic.LoadInt32(&loaderCalls), "Loader should NOT be called on negative cache hit")

	// 3. Wait for hard expiration (150ms > 120ms)
	time.Sleep(150 * time.Millisecond)

	// 4. Third call: hard expired, loader is invoked again
	_, err3 := cache.GetOrSet(c, ctx, "missing-entity-123", loader, cache.Options{
		NegativeCache:   true,
		NegativeSoftTTL: 50 * time.Millisecond,
		NegativeHardTTL: 120 * time.Millisecond,
	})
	assert.ErrorIs(t, err3, cache.ErrNotFound)
	assert.Equal(t, int32(2), atomic.LoadInt32(&loaderCalls), "Loader must be called after negative cache hard expiration")
}

func TestCacheMemoryLRUEviction(t *testing.T) {
	c := cache.NewMemoryCache(cache.Config{
		MaxMemoryEntries: 2,
		TTLJitterRatio:   0,
	})

	ctx := context.Background()
	dummyLoader := func(val string) func() (string, error) {
		return func() (string, error) {
			return val, nil
		}
	}

	// Insert item1 and item2
	_, err := cache.GetOrSet(c, ctx, "k1", dummyLoader("v1"), cache.Options{SoftTTL: time.Hour, HardTTL: time.Hour})
	require.NoError(t, err)
	_, err = cache.GetOrSet(c, ctx, "k2", dummyLoader("v2"), cache.Options{SoftTTL: time.Hour, HardTTL: time.Hour})
	require.NoError(t, err)

	// Access k1, making k2 the LRU
	_, err = cache.GetOrSet(c, ctx, "k1", dummyLoader("v1-new"), cache.Options{SoftTTL: time.Hour, HardTTL: time.Hour})
	require.NoError(t, err)

	// Insert k3: capacity is 2, so k2 must be evicted
	_, err = cache.GetOrSet(c, ctx, "k3", dummyLoader("v3"), cache.Options{SoftTTL: time.Hour, HardTTL: time.Hour})
	require.NoError(t, err)

	// Check k1 is still present in cache (raw Get returns v1)
	rawK1, foundK1, err := c.Get(ctx, "k1")
	require.NoError(t, err)
	assert.True(t, foundK1)
	assert.Contains(t, string(rawK1), "v1")

	// Check k2 was evicted
	_, foundK2, err := c.Get(ctx, "k2")
	require.NoError(t, err)
	assert.False(t, foundK2, "k2 should have been evicted by LRU")
}

func TestCacheRawGetSetDelete(t *testing.T) {
	c := cache.NewMemoryCache(cache.Config{
		TTLJitterRatio: 0,
	})
	ctx := context.Background()

	// 1. Get non-existent
	data, found, err := c.Get(ctx, "non-existent")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, data)

	// 2. Set raw bytes
	err = c.Set(ctx, "raw-key", []byte("raw-payload"), 10*time.Minute)
	require.NoError(t, err)

	// 3. Get raw bytes
	data, found, err = c.Get(ctx, "raw-key")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, []byte("raw-payload"), data)

	// 4. Delete key
	err = c.Delete(ctx, "raw-key")
	require.NoError(t, err)

	// 5. Verify deleted
	data, found, err = c.Get(ctx, "raw-key")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, data)
}

func TestCacheNewFromAppConfig(t *testing.T) {
	appCfg := config.CacheConfig{
		TTLJitterRatio:     0.20,
		DefaultSoftTTLSec:  120,
		DefaultHardTTLSec:  600,
		NegativeSoftTTLSec: 30,
	}

	c := cache.NewFromAppConfig(appCfg, nil)
	require.NotNil(t, c)

	cfg := c.Config()
	assert.Equal(t, 0.20, cfg.TTLJitterRatio)
	assert.Equal(t, 120*time.Second, cfg.DefaultSoftTTL)
	assert.Equal(t, 600*time.Second, cfg.DefaultHardTTL)
	assert.Equal(t, 30*time.Second, cfg.NegativeSoftTTL)
	assert.Nil(t, c.Redis())
	assert.NotNil(t, c.Singleflight())
}

// mockLockRedis is a lightweight mock for goredis.Cmdable supporting Get, Set, Del, SetNX, and Eval/EvalSha.
type mockLockRedis struct {
	goredis.Cmdable
	mu    sync.Mutex
	store map[string]string
}

func newMockLockRedis() *mockLockRedis {
	return &mockLockRedis{
		store: make(map[string]string),
	}
}

func (m *mockLockRedis) Get(ctx context.Context, key string) *goredis.StringCmd {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := goredis.NewStringCmd(ctx)
	if val, ok := m.store[key]; ok {
		cmd.SetVal(val)
	} else {
		cmd.SetErr(goredis.Nil)
	}
	return cmd
}

func (m *mockLockRedis) Set(ctx context.Context, key string, value any, expiration time.Duration) *goredis.StatusCmd {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := goredis.NewStatusCmd(ctx)
	switch v := value.(type) {
	case string:
		m.store[key] = v
	case []byte:
		m.store[key] = string(v)
	default:
		m.store[key] = string(fmt.Sprint(v))
	}
	cmd.SetVal("OK")
	return cmd
}

func (m *mockLockRedis) Del(ctx context.Context, keys ...string) *goredis.IntCmd {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := goredis.NewIntCmd(ctx)
	var count int64
	for _, key := range keys {
		if _, ok := m.store[key]; ok {
			delete(m.store, key)
			count++
		}
	}
	cmd.SetVal(count)
	return cmd
}

func (m *mockLockRedis) SetNX(ctx context.Context, key string, value any, expiration time.Duration) *goredis.BoolCmd {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := goredis.NewBoolCmd(ctx)
	if _, exists := m.store[key]; exists {
		cmd.SetVal(false)
	} else {
		m.store[key] = value.(string)
		cmd.SetVal(true)
	}
	return cmd
}

func (m *mockLockRedis) EvalSha(ctx context.Context, sha string, keys []string, args ...any) *goredis.Cmd {
	return m.Eval(ctx, "", keys, args...)
}

func (m *mockLockRedis) Eval(ctx context.Context, script string, keys []string, args ...any) *goredis.Cmd {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd := goredis.NewCmd(ctx)
	if len(keys) > 0 && len(args) > 0 {
		key := keys[0]
		expectedToken := args[0].(string)
		if curVal, ok := m.store[key]; ok && curVal == expectedToken {
			delete(m.store, key)
			cmd.SetVal(int64(1))
			return cmd
		}
	}
	cmd.SetVal(int64(0))
	return cmd
}

func TestRedisDistributedLockAcquireAndRelease(t *testing.T) {
	mockRedis := newMockLockRedis()
	ctx := context.Background()

	// 1. Acquire lock
	lock1, err := cache.TryAcquireLock(ctx, mockRedis, "order:101", 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, lock1)
	assert.NotEmpty(t, lock1.Token())
	assert.Equal(t, "order:101:lock", lock1.Key())

	// 2. Second attempt must fail with ErrLockNotAcquired
	lock2, err := cache.TryAcquireLock(ctx, mockRedis, "order:101", 30*time.Second)
	assert.ErrorIs(t, err, cache.ErrLockNotAcquired)
	assert.Nil(t, lock2)

	// 3. Release lock1
	err = lock1.Unlock(ctx)
	require.NoError(t, err)

	// 4. Now acquire should succeed again
	lock3, err := cache.TryAcquireLock(ctx, mockRedis, "order:101", 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, lock3)
	assert.NotEqual(t, lock1.Token(), lock3.Token(), "New lock must have new unique token")

	// Cleanup
	_ = lock3.Unlock(ctx)
}

func TestRedisLockNilClient(t *testing.T) {
	ctx := context.Background()
	lock, err := cache.TryAcquireLock(ctx, nil, "any-key", 10*time.Second)
	assert.ErrorIs(t, err, cache.ErrNilRedisClient)
	assert.Nil(t, lock)

	// Safe unlock on nil lock
	var nilLock *cache.RedisLock
	err = nilLock.Unlock(ctx)
	assert.NoError(t, err)
}

func TestEnvelopeExpirationHelpers(t *testing.T) {
	now := time.Now()
	env := &cache.Envelope{
		CreatedAt: now.Add(-50 * time.Millisecond),
		SoftTTL:   int64(30 * time.Millisecond),
		HardTTL:   int64(100 * time.Millisecond),
	}

	assert.False(t, env.IsFresh(now))
	assert.True(t, env.IsStale(now))
	assert.False(t, env.IsExpired(now))

	futureTime := now.Add(60 * time.Millisecond) // age = 110ms > HardTTL 100ms
	assert.True(t, env.IsExpired(futureTime))
}

func TestLoaderErrorPropagation(t *testing.T) {
	c := cache.NewMemoryCache(cache.Config{
		TTLJitterRatio: 0,
	})
	ctx := context.Background()

	expectedErr := errors.New("database connection refused")
	loader := func() (string, error) {
		return "", expectedErr
	}

	val, err := cache.GetOrSet(c, ctx, "failing-key", loader, cache.Options{})
	assert.ErrorIs(t, err, expectedErr)
	assert.Empty(t, val)

	// Verify not cached: error loader should run again
	calls := 0
	reLoader := func() (string, error) {
		calls++
		return "recovered", nil
	}

	val, err = cache.GetOrSet(c, ctx, "failing-key", reLoader, cache.Options{})
	require.NoError(t, err)
	assert.Equal(t, "recovered", val)
	assert.Equal(t, 1, calls)
}

func TestCacheSWRPanicShield(t *testing.T) {
	c := cache.NewMemoryCache(cache.Config{
		DisableJitter: true,
	})
	ctx := context.Background()

	var panicTriggered atomic.Bool

	loader := func() (string, error) {
		if panicTriggered.Load() {
			panic("simulated fatal background loader disaster")
		}
		return "initial-value", nil
	}

	// 1. Initial population: soft 30ms, hard 300ms
	val1, err := cache.GetOrSet(c, ctx, "panic-key", loader, cache.Options{
		SoftTTL: 30 * time.Millisecond,
		HardTTL: 300 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, "initial-value", val1)

	// 2. Wait until soft TTL expires (stale)
	time.Sleep(40 * time.Millisecond)

	// Arm loader to panic during background revalidation
	panicTriggered.Store(true)

	// 3. Stale hit: caller receives initial-value immediately, background revalidate panics
	val2, err := cache.GetOrSet(c, ctx, "panic-key", loader, cache.Options{
		SoftTTL: 30 * time.Millisecond,
		HardTTL: 300 * time.Millisecond,
	})
	require.NoError(t, err)
	assert.Equal(t, "initial-value", val2, "Must return stale value even if revalidation will panic")

	// Allow background goroutine to execute and recover safely
	time.Sleep(50 * time.Millisecond)

	// 4. Verify cache still retains stale value and process did not crash
	rawVal, found, err := c.Get(ctx, "panic-key")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Contains(t, string(rawVal), "initial-value")
}

func TestCacheLockPollingContextCancellation(t *testing.T) {
	mockRedis := newMockLockRedis()
	ctx := context.Background()

	// Pre-acquire lock to simulate another node holding it
	lock1, err := cache.TryAcquireLock(ctx, mockRedis, "contended-key", 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, lock1)

	c := cache.New(cache.Config{
		LoaderWaitTimeout: 5 * time.Second,
	}, mockRedis)

	var loaderCalls int32
	loader := func() (string, error) {
		atomic.AddInt32(&loaderCalls, 1)
		return "should-not-be-called", nil
	}

	// Call GetOrSet with a short canceled context
	cancelingCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	val, err := cache.GetOrSet(c, cancelingCtx, "contended-key", loader, cache.Options{
		SoftTTL: time.Minute,
		HardTTL: 5 * time.Minute,
	})

	// Must fail with context error and must NOT fall through to execute loader
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
	assert.Empty(t, val)
	assert.Equal(t, int32(0), atomic.LoadInt32(&loaderCalls), "Loader must NOT be called on context cancellation during lock polling")

	_ = lock1.Unlock(ctx)
}

func TestCacheRedisBackedGetOrSet(t *testing.T) {
	mockRedis := newMockLockRedis()
	cfg := cache.Config{
		DefaultSoftTTL: time.Minute,
		DefaultHardTTL: 5 * time.Minute,
		DisableJitter:  true,
	}

	c1 := cache.New(cfg, mockRedis)

	var loader1Calls int32
	loader1 := func() (string, error) {
		atomic.AddInt32(&loader1Calls, 1)
		return "redis-cached-val", nil
	}

	ctx := context.Background()

	// 1. Initial call on Node 1: cache miss, executes loader, writes to L1 & Redis
	val1, err := cache.GetOrSet(c1, ctx, "cross-node-key", loader1, cache.Options{})
	require.NoError(t, err)
	assert.Equal(t, "redis-cached-val", val1)
	assert.Equal(t, int32(1), atomic.LoadInt32(&loader1Calls))

	// Verify data is stored in mock Redis
	rawRedis, found, err := c1.GetEnvelope(ctx, "cross-node-key")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Contains(t, string(rawRedis.Data), "redis-cached-val")

	// 2. Node 2 starts with cold local memory, sharing the same Redis
	c2 := cache.New(cfg, mockRedis)

	var loader2Calls int32
	loader2 := func() (string, error) {
		atomic.AddInt32(&loader2Calls, 1)
		return "should-not-load-on-hit", nil
	}

	val2, err := cache.GetOrSet(c2, ctx, "cross-node-key", loader2, cache.Options{})
	require.NoError(t, err)
	assert.Equal(t, "redis-cached-val", val2, "Must serve from L2 Redis backing store")
	assert.Equal(t, int32(0), atomic.LoadInt32(&loader2Calls), "Loader must not be called when data exists in Redis")
}
