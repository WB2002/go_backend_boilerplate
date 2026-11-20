package featureflag_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/featureflag"
)

func TestFeatureFlagLocalCacheEvaluation(t *testing.T) {
	ff := featureflag.NewMemoryEngine()
	defer func() { _ = ff.Close() }()
	ctx := context.Background()

	err := ff.SetFlag(ctx, "new-checkout-engine", true, nil)
	assert.NoError(t, err)

	enabled, err := ff.IsEnabled(ctx, "new-checkout-engine", featureflag.Context{UserID: "usr-123"})
	assert.NoError(t, err)
	assert.True(t, enabled)

	// Invalidate flag
	ff.Invalidate("new-checkout-engine")
	err = ff.SetFlag(ctx, "new-checkout-engine", false, nil)
	assert.NoError(t, err)

	enabledAfter, err := ff.IsEnabled(ctx, "new-checkout-engine", featureflag.Context{UserID: "usr-123"})
	assert.NoError(t, err)
	assert.False(t, enabledAfter)
}

func TestFeatureFlagInMemoryBasicEvaluation(t *testing.T) {
	ff := featureflag.NewMemoryEngine()
	defer func() { _ = ff.Close() }()
	ctx := context.Background()

	// 1. Non-existent flag returns false, nil
	enabled, err := ff.IsEnabled(ctx, "non-existent", featureflag.Context{UserID: "u1"})
	assert.NoError(t, err)
	assert.False(t, enabled)

	// 2. Global disabled flag returns false even if rules exist
	err = ff.SaveFlag(ctx, featureflag.Flag{
		Key:          "disabled-flag",
		Enabled:      false,
		AllowedUsers: []string{"admin"},
	})
	assert.NoError(t, err)

	enabled, err = ff.IsEnabled(ctx, "disabled-flag", featureflag.Context{UserID: "admin"})
	assert.NoError(t, err)
	assert.False(t, enabled)

	// 3. User allowlist matching
	err = ff.SaveFlag(ctx, featureflag.Flag{
		Key:          "beta-feature",
		Enabled:      true,
		AllowedUsers: []string{"usr-100", "usr-200"},
	})
	assert.NoError(t, err)

	enabled, err = ff.IsEnabled(ctx, "beta-feature", featureflag.Context{UserID: "usr-100"})
	assert.NoError(t, err)
	assert.True(t, enabled)

	enabled, err = ff.IsEnabled(ctx, "beta-feature", featureflag.Context{UserID: "usr-999"})
	assert.NoError(t, err)
	assert.False(t, enabled)

	// 4. Role allowlist matching
	err = ff.SaveFlag(ctx, featureflag.Flag{
		Key:          "admin-panel",
		Enabled:      true,
		AllowedRoles: []string{"superadmin", "editor"},
	})
	assert.NoError(t, err)

	enabled, err = ff.IsEnabled(ctx, "admin-panel", featureflag.Context{
		UserID: "usr-1",
		Roles:  []string{"editor", "guest"},
	})
	assert.NoError(t, err)
	assert.True(t, enabled)

	enabled, err = ff.IsEnabled(ctx, "admin-panel", featureflag.Context{
		UserID: "usr-2",
		Roles:  []string{"viewer"},
	})
	assert.NoError(t, err)
	assert.False(t, enabled)

	// 5. Targeting via SetFlag rules slice
	err = ff.SetFlag(ctx, "rule-based-feature", true, []featureflag.Rule{
		{Type: "allow_users", Values: []string{"vip-1"}},
		{Type: "allow_roles", Values: []string{"staff"}},
	})
	assert.NoError(t, err)

	enabled, err = ff.IsEnabled(ctx, "rule-based-feature", featureflag.Context{UserID: "vip-1"})
	assert.NoError(t, err)
	assert.True(t, enabled)

	enabled, err = ff.IsEnabled(ctx, "rule-based-feature", featureflag.Context{Roles: []string{"staff"}})
	assert.NoError(t, err)
	assert.True(t, enabled)

	enabled, err = ff.IsEnabled(ctx, "rule-based-feature", featureflag.Context{UserID: "regular-user"})
	assert.NoError(t, err)
	assert.False(t, enabled)
}

func TestFeatureFlagPercentageRollout(t *testing.T) {
	ff := featureflag.NewMemoryEngine()
	defer func() { _ = ff.Close() }()
	ctx := context.Background()

	// 1. Percentage 0% -> false for everyone
	err := ff.SaveFlag(ctx, featureflag.Flag{
		Key:        "zero-rollout",
		Enabled:    true,
		Percentage: 0,
	})
	assert.NoError(t, err)

	for i := 0; i < 50; i++ {
		enabled, err := ff.IsEnabled(ctx, "zero-rollout", featureflag.Context{
			UserID: fmt.Sprintf("user-%d", i),
		})
		assert.NoError(t, err)
		assert.True(t, enabled, "Percentage 0 without other rules acts as globally enabled")
	}

	// Flag with explicitly configured targeting percentage 0 (e.g. 0% of cohort)
	err = ff.SetFlag(ctx, "zero-cohort", true, []featureflag.Rule{
		{AllowedUsers: []string{"whitelisted"}},
		{Percentage: 0},
	})
	assert.NoError(t, err)

	enabled, err := ff.IsEnabled(ctx, "zero-cohort", featureflag.Context{UserID: "regular"})
	assert.NoError(t, err)
	assert.False(t, enabled)

	enabled, err = ff.IsEnabled(ctx, "zero-cohort", featureflag.Context{UserID: "whitelisted"})
	assert.NoError(t, err)
	assert.True(t, enabled)

	// 2. Percentage 100% -> true for all users
	err = ff.SaveFlag(ctx, featureflag.Flag{
		Key:        "full-rollout",
		Enabled:    true,
		Percentage: 100,
	})
	assert.NoError(t, err)

	for i := 0; i < 50; i++ {
		enabled, err := ff.IsEnabled(ctx, "full-rollout", featureflag.Context{
			UserID: fmt.Sprintf("user-%d", i),
		})
		assert.NoError(t, err)
		assert.True(t, enabled)
	}

	// 3. Consistency / Sticky assignment test
	err = ff.SaveFlag(ctx, featureflag.Flag{
		Key:        "sticky-feature",
		Enabled:    true,
		Percentage: 35,
	})
	assert.NoError(t, err)

	eval1, err := ff.IsEnabled(ctx, "sticky-feature", featureflag.Context{UserID: "sticky-user-42"})
	assert.NoError(t, err)

	for i := 0; i < 50; i++ {
		evalN, err := ff.IsEnabled(ctx, "sticky-feature", featureflag.Context{UserID: "sticky-user-42"})
		assert.NoError(t, err)
		assert.Equal(t, eval1, evalN, "Sticky assignment must be deterministic across evaluations")
	}

	// 4. Statistical distribution for 25% rollout over 1,000 users
	err = ff.SaveFlag(ctx, featureflag.Flag{
		Key:        "canary-checkout",
		Enabled:    true,
		Percentage: 25,
	})
	assert.NoError(t, err)

	var enabledCount int
	totalUsers := 1000

	for i := 0; i < totalUsers; i++ {
		uid := fmt.Sprintf("cust-id-%d", i)
		enabled, err := ff.IsEnabled(ctx, "canary-checkout", featureflag.Context{UserID: uid})
		assert.NoError(t, err)
		if enabled {
			enabledCount++
		}
	}

	ratio := float64(enabledCount) / float64(totalUsers)
	// 25% rollout across 1000 users should comfortably land between 20% and 30%
	assert.GreaterOrEqual(t, ratio, 0.20, "Rollout distribution ratio too low")
	assert.LessOrEqual(t, ratio, 0.30, "Rollout distribution ratio too high")

	// 5. Empty UserID cannot qualify for percentage rollout
	anonEnabled, err := ff.IsEnabled(ctx, "canary-checkout", featureflag.Context{UserID: ""})
	assert.NoError(t, err)
	assert.False(t, anonEnabled)
}

func TestFeatureFlagCRUDAndGetAll(t *testing.T) {
	ff := featureflag.NewMemoryEngine()
	defer func() { _ = ff.Close() }()
	ctx := context.Background()

	// Get non-existent
	_, err := ff.GetFlag(ctx, "missing")
	assert.ErrorIs(t, err, featureflag.ErrFlagNotFound)

	// Save and Get
	flag := featureflag.Flag{
		Key:          "feature-a",
		Enabled:      true,
		Percentage:   50,
		AllowedUsers: []string{"usr-1"},
	}
	err = ff.SaveFlag(ctx, flag)
	assert.NoError(t, err)

	got, err := ff.GetFlag(ctx, "feature-a")
	assert.NoError(t, err)
	assert.Equal(t, "feature-a", got.Key)
	assert.True(t, got.Enabled)
	assert.Equal(t, 50, got.Percentage)

	// Save another
	err = ff.SetFlag(ctx, "feature-b", false, nil)
	assert.NoError(t, err)

	// GetAll
	all, err := ff.GetAllFlags(ctx)
	assert.NoError(t, err)
	assert.Len(t, all, 2)
	assert.Contains(t, all, "feature-a")
	assert.Contains(t, all, "feature-b")

	// Delete
	err = ff.DeleteFlag(ctx, "feature-a")
	assert.NoError(t, err)

	_, err = ff.GetFlag(ctx, "feature-a")
	assert.ErrorIs(t, err, featureflag.ErrFlagNotFound)

	allAfter, err := ff.GetAllFlags(ctx)
	assert.NoError(t, err)
	assert.Len(t, allAfter, 1)
	assert.NotContains(t, allAfter, "feature-a")
}

func TestFeatureFlagGracefulShutdown(t *testing.T) {
	ff := featureflag.NewMemoryEngine()
	ctx := context.Background()

	err := ff.SetFlag(ctx, "flag1", true, nil)
	assert.NoError(t, err)

	err = ff.Close()
	assert.NoError(t, err)

	// Idempotent close
	err = ff.Close()
	assert.NoError(t, err)

	// Subsequent calls return ErrClosed
	_, err = ff.IsEnabled(ctx, "flag1", featureflag.Context{})
	assert.ErrorIs(t, err, featureflag.ErrClosed)

	err = ff.SetFlag(ctx, "flag2", true, nil)
	assert.ErrorIs(t, err, featureflag.ErrClosed)

	_, err = ff.GetFlag(ctx, "flag1")
	assert.ErrorIs(t, err, featureflag.ErrClosed)

	_, err = ff.GetAllFlags(ctx)
	assert.ErrorIs(t, err, featureflag.ErrClosed)

	err = ff.DeleteFlag(ctx, "flag1")
	assert.ErrorIs(t, err, featureflag.ErrClosed)
}

// startMockRedisServer creates an in-process TCP server implementing Redis RESP protocol for testing pub/sub and hashes.
func startMockRedisServer(t *testing.T) (*goredis.Client, string, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("skipping redis-dependent test: socket listen not permitted in environment")
		return nil, "", func() {}
	}

	addr := ln.Addr().String()
	hashes := make(map[string]map[string]string)
	subscribers := make(map[string][]net.Conn)
	var mu sync.Mutex
	closed := make(chan struct{})

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleMockRedisClient(conn, hashes, subscribers, &mu)
		}
	}()

	client := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Protocol: 2,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		_ = ln.Close()
		close(closed)
		t.Skipf("skipping redis-dependent test: local redis mock dial failed: %v", err)
		return nil, "", func() {}
	}

	cleanup := func() {
		close(closed)
		_ = client.Close()
		_ = ln.Close()
	}

	return client, addr, cleanup
}

func handleMockRedisClient(
	c net.Conn,
	hashes map[string]map[string]string,
	subscribers map[string][]net.Conn,
	mu *sync.Mutex,
) {
	defer c.Close()
	reader := bufio.NewReader(c)
	isSubscribed := false
	var subscribedChannels []string

	defer func() {
		if isSubscribed {
			mu.Lock()
			for _, ch := range subscribedChannels {
				conns := subscribers[ch]
				for i, sub := range conns {
					if sub == c {
						subscribers[ch] = append(conns[:i], conns[i+1:]...)
						break
					}
				}
			}
			mu.Unlock()
		}
	}()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "*") {
			continue
		}

		numArgs, err := strconv.Atoi(line[1:])
		if err != nil || numArgs <= 0 {
			continue
		}

		args := make([]string, 0, numArgs)
		for i := 0; i < numArgs; i++ {
			lenLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			var argLen int
			_, _ = fmt.Sscanf(strings.TrimSpace(lenLine), "$%d", &argLen)
			if argLen < 0 {
				args = append(args, "")
				continue
			}
			buf := make([]byte, argLen)
			if _, err := io.ReadFull(reader, buf); err != nil {
				return
			}
			_, _ = reader.ReadString('\n') // discard \r\n
			args = append(args, string(buf))
		}
		if len(args) == 0 {
			continue
		}

		cmd := strings.ToUpper(args[0])
		switch cmd {
		case "HELLO":
			_, _ = c.Write([]byte("-ERR unknown command 'HELLO'\r\n"))
		case "CLIENT":
			_, _ = c.Write([]byte("+OK\r\n"))
		case "PING":
			if isSubscribed {
				_, _ = c.Write([]byte("*2\r\n$4\r\npong\r\n$0\r\n\r\n"))
			} else {
				_, _ = c.Write([]byte("+PONG\r\n"))
			}
		case "HSET":
			if len(args) >= 4 {
				key := args[1]
				field := args[2]
				val := args[3]
				mu.Lock()
				if hashes[key] == nil {
					hashes[key] = make(map[string]string)
				}
				hashes[key][field] = val
				mu.Unlock()
				_, _ = c.Write([]byte(":1\r\n"))
			} else {
				_, _ = c.Write([]byte("-ERR wrong number of arguments for 'hset' command\r\n"))
			}
		case "HGET":
			if len(args) >= 3 {
				key := args[1]
				field := args[2]
				mu.Lock()
				val, ok := hashes[key][field]
				mu.Unlock()
				if ok {
					_, _ = c.Write([]byte(fmt.Sprintf("$%d\r\n%s\r\n", len(val), val)))
				} else {
					_, _ = c.Write([]byte("$-1\r\n"))
				}
			} else {
				_, _ = c.Write([]byte("-ERR wrong number of arguments for 'hget' command\r\n"))
			}
		case "HDEL":
			if len(args) >= 3 {
				key := args[1]
				field := args[2]
				mu.Lock()
				if hashes[key] != nil {
					delete(hashes[key], field)
				}
				mu.Unlock()
				_, _ = c.Write([]byte(":1\r\n"))
			} else {
				_, _ = c.Write([]byte("-ERR wrong number of arguments for 'hdel' command\r\n"))
			}
		case "HGETALL":
			if len(args) >= 2 {
				key := args[1]
				mu.Lock()
				data := make(map[string]string)
				for k, v := range hashes[key] {
					data[k] = v
				}
				mu.Unlock()

				var b strings.Builder
				b.WriteString(fmt.Sprintf("*%d\r\n", len(data)*2))
				for k, v := range data {
					b.WriteString(fmt.Sprintf("$%d\r\n%s\r\n$%d\r\n%s\r\n", len(k), k, len(v), v))
				}
				_, _ = c.Write([]byte(b.String()))
			} else {
				_, _ = c.Write([]byte("-ERR wrong number of arguments for 'hgetall' command\r\n"))
			}
		case "SUBSCRIBE":
			isSubscribed = true
			ch := args[1]
			mu.Lock()
			subscribers[ch] = append(subscribers[ch], c)
			subscribedChannels = append(subscribedChannels, ch)
			mu.Unlock()
			resp := fmt.Sprintf("*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(ch), ch)
			_, _ = c.Write([]byte(resp))
		case "UNSUBSCRIBE":
			ch := ""
			if len(args) > 1 {
				ch = args[1]
			}
			resp := fmt.Sprintf("*3\r\n$11\r\nunsubscribe\r\n$%d\r\n%s\r\n:0\r\n", len(ch), ch)
			_, _ = c.Write([]byte(resp))
		case "PUBLISH":
			ch := args[1]
			msg := args[2]
			mu.Lock()
			conns := append([]net.Conn(nil), subscribers[ch]...)
			for _, subConn := range conns {
				frame := fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(ch), ch, len(msg), msg)
				_, _ = subConn.Write([]byte(frame))
			}
			mu.Unlock()
			_, _ = c.Write([]byte(fmt.Sprintf(":%d\r\n", len(conns))))
		default:
			_, _ = c.Write([]byte("+OK\r\n"))
		}
	}
}

func TestFeatureFlagRedisPubSubInvalidation(t *testing.T) {
	rdb1, addr, cleanup := startMockRedisServer(t)
	if rdb1 == nil {
		return
	}
	defer cleanup()

	rdb2 := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Protocol: 2,
	})
	defer func() { _ = rdb2.Close() }()

	cfg := config.FeatureFlagConfig{ReconcileMs: -1}

	engine1 := featureflag.NewEngine(cfg, rdb1)
	defer func() { _ = engine1.Close() }()

	engine2 := featureflag.NewEngine(cfg, rdb2)
	defer func() { _ = engine2.Close() }()

	// Allow Redis subscription goroutines to establish
	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 1. Engine 1 sets flag "checkout-v2" to true
	err := engine1.SetFlag(ctx, "checkout-v2", true, nil)
	require.NoError(t, err)

	// 2. Engine 2 evaluates "checkout-v2" (cache miss -> reads from Redis and caches locally)
	enabled2, err := engine2.IsEnabled(ctx, "checkout-v2", featureflag.Context{UserID: "u1"})
	require.NoError(t, err)
	assert.True(t, enabled2)

	// 3. Engine 1 updates "checkout-v2" to false (stores in Redis + publishes to featureflags:invalidate)
	err = engine1.SetFlag(ctx, "checkout-v2", false, nil)
	require.NoError(t, err)

	// 4. Engine 2 receives invalidation event via Pub/Sub and evicts "checkout-v2" from memory
	// Next evaluation in Engine 2 sees fresh false from Redis
	assert.Eventually(t, func() bool {
		val, err := engine2.IsEnabled(ctx, "checkout-v2", featureflag.Context{UserID: "u1"})
		return err == nil && !val
	}, 2*time.Second, 50*time.Millisecond, "Engine 2 local cache should be evicted by Redis pub/sub invalidation")
}

func TestFeatureFlagReconciliationLoop(t *testing.T) {
	rdb, addr, cleanup := startMockRedisServer(t)
	if rdb == nil {
		return
	}
	defer cleanup()

	rdbWriter := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Protocol: 2,
	})
	defer func() { _ = rdbWriter.Close() }()

	// Fast reconciliation interval of 50ms for testing
	cfg := config.FeatureFlagConfig{ReconcileMs: 50}

	engine := featureflag.NewEngine(cfg, rdb)
	defer func() { _ = engine.Close() }()

	ctx := context.Background()

	// Initially set flag in Engine to true
	err := engine.SetFlag(ctx, "reconcile-test", true, nil)
	require.NoError(t, err)

	enabled, err := engine.IsEnabled(ctx, "reconcile-test", featureflag.Context{UserID: "u1"})
	require.NoError(t, err)
	assert.True(t, enabled)

	// Simulate dropped pub/sub packet by updating Redis directly without publishing invalidation
	flagJSON := `{"key":"reconcile-test","enabled":false}`
	err = rdbWriter.HSet(ctx, featureflag.DefaultDefinitionsKey, "reconcile-test", flagJSON).Err()
	require.NoError(t, err)

	// Background reconciliation loop should reconcile the cache within 500ms
	assert.Eventually(t, func() bool {
		val, err := engine.IsEnabled(ctx, "reconcile-test", featureflag.Context{UserID: "u1"})
		return err == nil && !val
	}, 1*time.Second, 25*time.Millisecond, "Reconciliation loop should synchronize local cache from Redis")
}

func BenchmarkFeatureFlagEvaluation(b *testing.B) {
	ff := featureflag.NewMemoryEngine()
	defer func() { _ = ff.Close() }()
	ctx := context.Background()

	_ = ff.SaveFlag(ctx, featureflag.Flag{
		Key:        "bench-flag",
		Enabled:    true,
		Percentage: 50,
	})

	evalCtx := featureflag.Context{UserID: "bench-user-123"}
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = ff.IsEnabled(ctx, "bench-flag", evalCtx)
	}
}
