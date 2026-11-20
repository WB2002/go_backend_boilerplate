package cache

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

var (
	// ErrLockNotAcquired indicates the distributed lock was not acquired (held by another process).
	ErrLockNotAcquired = errors.New("cache: distributed lock not acquired")

	// ErrNilRedisClient indicates the Redis client is nil.
	ErrNilRedisClient = errors.New("cache: redis client is nil")
)

// unlockLuaScript safely releases a distributed lock only if the token matches.
const unlockLuaScript = `
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end
`

var unlockScript = goredis.NewScript(unlockLuaScript)

// DistributedLock represents an acquired distributed lock.
type DistributedLock interface {
	Unlock(ctx context.Context) error
	Token() string
	Key() string
}

// RedisLock holds state for an acquired Redis-backed distributed lock.
type RedisLock struct {
	client  goredis.Cmdable
	lockKey string
	token   string
}

// Token returns the unique ownership token of the lock.
func (l *RedisLock) Token() string {
	return l.token
}

// Key returns the Redis key of the lock.
func (l *RedisLock) Key() string {
	return l.lockKey
}

// Unlock releases the lock if the token matches the value currently in Redis.
func (l *RedisLock) Unlock(ctx context.Context) error {
	if l == nil || l.client == nil {
		return nil
	}
	_, err := unlockScript.Run(ctx, l.client, []string{l.lockKey}, l.token).Result()
	return err
}

// TryAcquireLock attempts to acquire a distributed lock on Redis with the specified TTL.
// Uses SET key:lock <token> NX PX <ttl>. Returns ErrLockNotAcquired if the lock is held.
func TryAcquireLock(ctx context.Context, client goredis.Cmdable, key string, ttl time.Duration) (*RedisLock, error) {
	if client == nil {
		return nil, ErrNilRedisClient
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}

	lockKey := key + ":lock"
	token := uuid.New().String()

	acquired, err := client.SetNX(ctx, lockKey, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrLockNotAcquired
	}

	return &RedisLock{
		client:  client,
		lockKey: lockKey,
		token:   token,
	}, nil
}
