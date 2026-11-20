package featureflag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/platform/redis"
)

const (
	// DefaultDefinitionsKey is the Redis hash storing all feature flag definitions.
	DefaultDefinitionsKey = "featureflags:definitions"
	// DefaultInvalidateChannel is the Redis Pub/Sub topic for cache invalidation events.
	DefaultInvalidateChannel = "featureflags:invalidate"
	// DefaultReconcileInterval is the fallback reconciliation ticker interval.
	DefaultReconcileInterval = 45 * time.Second
)

var (
	// ErrFlagNotFound indicates the requested feature flag definition does not exist.
	ErrFlagNotFound = errors.New("feature flag not found")
	// ErrClosed indicates the feature flag engine is shut down.
	ErrClosed = errors.New("feature flag engine is closed")
)

// Flag represents a feature flag definition with multi-condition targeting rules.
type Flag struct {
	Key          string            `json:"key"`
	Enabled      bool              `json:"enabled"`
	Percentage   int               `json:"percentage"`
	AllowedUsers []string          `json:"allowed_users,omitempty"`
	AllowedRoles []string          `json:"allowed_roles,omitempty"`
	Rules        []Rule            `json:"rules,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// Rule defines targeting criteria that can be evaluated on a flag.
type Rule struct {
	Type         string   `json:"type,omitempty"`
	Values       []string `json:"values,omitempty"`
	Percentage   int      `json:"percentage,omitempty"`
	AllowedUsers []string `json:"allowed_users,omitempty"`
	AllowedRoles []string `json:"allowed_roles,omitempty"`
}

// Context contains contextual information about the subject being evaluated.
type Context struct {
	UserID     string            `json:"user_id"`
	Roles      []string          `json:"roles,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// EvaluationContext is an alias for Context.
type EvaluationContext = Context

// Engine provides sub-microsecond in-memory feature flag evaluations with Redis backing and Pub/Sub invalidation.
type Engine struct {
	mu     sync.RWMutex
	cache  map[string]*Flag
	redis  *redis.Client
	pubsub *goredis.PubSub
	cfg    config.FeatureFlagConfig
	stopCh chan struct{}
	wg     sync.WaitGroup
	closed atomic.Bool
}

// NewEngine creates and initializes a feature flag engine. If redisClient is nil, it runs purely in-memory.
func NewEngine(cfg config.FeatureFlagConfig, rdb *redis.Client) *Engine {
	e := &Engine{
		cache:  make(map[string]*Flag),
		redis:  rdb,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}

	if rdb != nil {
		// Populate initial cache from Redis definitions
		initCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = e.Reconcile(initCtx)
		cancel()

		// Start Pub/Sub invalidation listener
		e.pubsub = rdb.Subscribe(context.Background(), DefaultInvalidateChannel)
		msgCh := e.pubsub.Channel()

		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			for {
				select {
				case <-e.stopCh:
					return
				case msg, ok := <-msgCh:
					if !ok {
						return
					}
					if msg != nil && msg.Payload != "" {
						e.Invalidate(msg.Payload)
					}
				}
			}
		}()

		// Start background reconciliation loop if configured
		if cfg.ReconcileMs >= 0 {
			interval := time.Duration(cfg.ReconcileMs) * time.Millisecond
			if interval == 0 {
				interval = DefaultReconcileInterval
			}

			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				ticker := time.NewTicker(interval)
				defer ticker.Stop()

				for {
					select {
					case <-e.stopCh:
						return
					case <-ticker.C:
						recCtx, recCancel := context.WithTimeout(context.Background(), 5*time.Second)
						_ = e.Reconcile(recCtx)
						recCancel()
					}
				}
			}()
		}
	}

	return e
}

// NewMemoryEngine creates a pure in-memory feature flag engine without Redis dependencies.
func NewMemoryEngine() *Engine {
	return NewEngine(config.FeatureFlagConfig{ReconcileMs: -1}, nil)
}

// IsEnabled evaluates whether a feature flag is enabled for the provided context.
// Evaluation is performed sub-microsecond against the local in-memory cache.
func (e *Engine) IsEnabled(ctx context.Context, flagKey string, evalCtx Context) (bool, error) {
	if e.closed.Load() {
		return false, ErrClosed
	}

	flag, err := e.GetFlag(ctx, flagKey)
	if err != nil {
		if errors.Is(err, ErrFlagNotFound) {
			return false, nil
		}
		return false, err
	}

	return e.evaluate(flag, evalCtx), nil
}

// SetFlag sets or updates a feature flag definition with the given enabled status and optional rules.
func (e *Engine) SetFlag(ctx context.Context, flagKey string, value bool, rules []Rule) error {
	flag := Flag{
		Key:     flagKey,
		Enabled: value,
		Rules:   rules,
	}

	for _, r := range rules {
		if r.Percentage > 0 {
			flag.Percentage = r.Percentage
		}
		if len(r.AllowedUsers) > 0 {
			flag.AllowedUsers = append(flag.AllowedUsers, r.AllowedUsers...)
		}
		if len(r.AllowedRoles) > 0 {
			flag.AllowedRoles = append(flag.AllowedRoles, r.AllowedRoles...)
		}
		if r.Type == "allow_users" || r.Type == "users" {
			flag.AllowedUsers = append(flag.AllowedUsers, r.Values...)
		}
		if r.Type == "allow_roles" || r.Type == "roles" {
			flag.AllowedRoles = append(flag.AllowedRoles, r.Values...)
		}
	}

	return e.SaveFlag(ctx, flag)
}

// SaveFlag persists a full flag definition to the backing store, invalidates local cache, and broadcasts Pub/Sub invalidation.
func (e *Engine) SaveFlag(ctx context.Context, flag Flag) error {
	if e.closed.Load() {
		return ErrClosed
	}
	if flag.Key == "" {
		return errors.New("flag key cannot be empty")
	}

	if flag.Percentage < 0 {
		flag.Percentage = 0
	} else if flag.Percentage > 100 {
		flag.Percentage = 100
	}

	if e.redis != nil {
		data, err := json.Marshal(flag)
		if err != nil {
			return fmt.Errorf("marshal flag: %w", err)
		}

		if err := e.redis.HSet(ctx, DefaultDefinitionsKey, flag.Key, string(data)).Err(); err != nil {
			return fmt.Errorf("redis hset: %w", err)
		}

		// Broadcast invalidation across nodes
		_ = e.redis.Publish(ctx, DefaultInvalidateChannel, flag.Key).Err()
	}

	// Update local cache
	flagCopy := flag
	e.mu.Lock()
	e.cache[flag.Key] = &flagCopy
	e.mu.Unlock()

	return nil
}

// GetFlag retrieves a feature flag by key. Checks local memory cache first, falling back to Redis if available.
func (e *Engine) GetFlag(ctx context.Context, flagKey string) (*Flag, error) {
	if e.closed.Load() {
		return nil, ErrClosed
	}

	// 1. Fast path: in-memory cache lookup
	e.mu.RLock()
	flag, ok := e.cache[flagKey]
	e.mu.RUnlock()
	if ok {
		if flag == nil {
			return nil, ErrFlagNotFound
		}
		cp := *flag
		return &cp, nil
	}

	// 2. Slow path: fetch from Redis if available
	if e.redis != nil {
		data, err := e.redis.HGet(ctx, DefaultDefinitionsKey, flagKey).Result()
		if err != nil {
			if errors.Is(err, goredis.Nil) {
				return nil, ErrFlagNotFound
			}
			return nil, fmt.Errorf("redis hget: %w", err)
		}

		var f Flag
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			return nil, fmt.Errorf("unmarshal flag: %w", err)
		}

		e.mu.Lock()
		e.cache[flagKey] = &f
		e.mu.Unlock()

		cp := f
		return &cp, nil
	}

	return nil, ErrFlagNotFound
}

// GetAllFlags retrieves all configured feature flags.
func (e *Engine) GetAllFlags(ctx context.Context) (map[string]Flag, error) {
	if e.closed.Load() {
		return nil, ErrClosed
	}

	if e.redis != nil {
		res, err := e.redis.HGetAll(ctx, DefaultDefinitionsKey).Result()
		if err != nil {
			return nil, fmt.Errorf("redis hgetall: %w", err)
		}
		result := make(map[string]Flag, len(res))
		for k, v := range res {
			var f Flag
			if err := json.Unmarshal([]byte(v), &f); err == nil {
				result[k] = f
			}
		}
		return result, nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make(map[string]Flag, len(e.cache))
	for k, v := range e.cache {
		if v != nil {
			result[k] = *v
		}
	}
	return result, nil
}

// DeleteFlag removes a flag definition from Redis and invalidates it from local cache.
func (e *Engine) DeleteFlag(ctx context.Context, flagKey string) error {
	if e.closed.Load() {
		return ErrClosed
	}
	if flagKey == "" {
		return errors.New("flag key cannot be empty")
	}

	if e.redis != nil {
		if err := e.redis.HDel(ctx, DefaultDefinitionsKey, flagKey).Err(); err != nil {
			return fmt.Errorf("redis hdel: %w", err)
		}
		_ = e.redis.Publish(ctx, DefaultInvalidateChannel, flagKey).Err()
	}

	e.Invalidate(flagKey)
	return nil
}

// Invalidate removes a flag from the in-memory cache, ensuring subsequent lookups re-fetch from Redis.
func (e *Engine) Invalidate(flagKey string) {
	e.mu.Lock()
	delete(e.cache, flagKey)
	e.mu.Unlock()
}

// Reconcile synchronizes the local in-memory cache with the current state in Redis.
func (e *Engine) Reconcile(ctx context.Context) error {
	if e.redis == nil {
		return nil
	}

	res, err := e.redis.HGetAll(ctx, DefaultDefinitionsKey).Result()
	if err != nil {
		return err
	}

	newCache := make(map[string]*Flag, len(res))
	for k, v := range res {
		var f Flag
		if err := json.Unmarshal([]byte(v), &f); err == nil {
			newCache[k] = &f
		}
	}

	e.mu.Lock()
	e.cache = newCache
	e.mu.Unlock()

	return nil
}

// Close gracefully terminates background subscription and reconciliation goroutines.
func (e *Engine) Close() error {
	if !e.closed.CompareAndSwap(false, true) {
		return nil
	}

	close(e.stopCh)

	var err error
	if e.pubsub != nil {
		err = e.pubsub.Close()
	}

	e.wg.Wait()
	return err
}

// evaluate applies targeting rules against a flag definition:
// 1. If flag is nil or not enabled globally, return false.
// 2. If AllowedUsers is set and UserID matches, return true.
// 3. If AllowedRoles is set and user has any matching role, return true.
// 4. If Percentage > 0, evaluate consistent hashing cohort.
// 5. If targeting criteria were specified but none matched, return false.
// 6. Otherwise return flag.Enabled.
func (e *Engine) evaluate(flag *Flag, evalCtx Context) bool {
	if flag == nil || !flag.Enabled {
		return false
	}

	// 1. Check user allowlist
	if evalCtx.UserID != "" {
		for _, u := range flag.AllowedUsers {
			if u == evalCtx.UserID {
				return true
			}
		}
		for _, r := range flag.Rules {
			for _, u := range r.AllowedUsers {
				if u == evalCtx.UserID {
					return true
				}
			}
			if r.Type == "allow_users" || r.Type == "users" {
				for _, u := range r.Values {
					if u == evalCtx.UserID {
						return true
					}
				}
			}
		}
	}

	// 2. Check role allowlist
	if len(evalCtx.Roles) > 0 {
		for _, userRole := range evalCtx.Roles {
			for _, allowedRole := range flag.AllowedRoles {
				if userRole == allowedRole {
					return true
				}
			}
			for _, r := range flag.Rules {
				for _, allowedRole := range r.AllowedRoles {
					if userRole == allowedRole {
						return true
					}
				}
				if r.Type == "allow_roles" || r.Type == "roles" {
					for _, allowedRole := range r.Values {
						if userRole == allowedRole {
							return true
						}
					}
				}
			}
		}
	}

	// 3. Check percentage rollout
	if flag.Percentage > 0 {
		if flag.Percentage >= 100 {
			return true
		}
		if evalCtx.UserID != "" {
			hashVal := hashUser(flag.Key, evalCtx.UserID)
			if hashVal < flag.Percentage {
				return true
			}
		}
	}
	for _, r := range flag.Rules {
		if r.Percentage > 0 {
			if r.Percentage >= 100 {
				return true
			}
			if evalCtx.UserID != "" {
				hashVal := hashUser(flag.Key, evalCtx.UserID)
				if hashVal < r.Percentage {
					return true
				}
			}
		}
	}

	// 4. If targeting rules were configured and none matched, return false
	hasTargeting := len(flag.AllowedUsers) > 0 || len(flag.AllowedRoles) > 0 || flag.Percentage > 0
	if !hasTargeting {
		for _, r := range flag.Rules {
			if len(r.AllowedUsers) > 0 || len(r.AllowedRoles) > 0 || r.Percentage > 0 || len(r.Values) > 0 {
				hasTargeting = true
				break
			}
		}
	}
	if hasTargeting {
		return false
	}

	// 5. Otherwise return global enabled status
	return flag.Enabled
}

// hashUser calculates a deterministic integer in [0, 99] using CRC32 IEEE consistent hashing.
func hashUser(flagKey, userID string) int {
	data := []byte(flagKey + ":" + userID)
	return int(crc32.ChecksumIEEE(data) % 100)
}
