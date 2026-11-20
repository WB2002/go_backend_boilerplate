package websocket_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go_backend_boilerplate/internal/config"
	"go_backend_boilerplate/internal/core/auth"
	"go_backend_boilerplate/internal/core/websocket"
)

type mockTokenManager struct {
	validateFunc func(ctx context.Context, token string) (*auth.Claims, error)
}

func (m *mockTokenManager) GenerateTokenPair(userID string, roles []string) (*auth.TokenPair, error) {
	return nil, nil
}

func (m *mockTokenManager) ValidateAccessToken(ctx context.Context, tokenStr string) (*auth.Claims, error) {
	if m.validateFunc != nil {
		return m.validateFunc(ctx, tokenStr)
	}
	if tokenStr == "valid-token" {
		return &auth.Claims{UserID: "user-test"}, nil
	}
	return nil, auth.ErrInvalidToken
}

func (m *mockTokenManager) ValidateRefreshToken(ctx context.Context, tokenStr string) (*auth.Claims, error) {
	return nil, nil
}

func (m *mockTokenManager) RevokeToken(ctx context.Context, jti string, ttl time.Duration) error {
	return nil
}

func TestWebSocketUserConnectionLimit(t *testing.T) {
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 2,
		MessagesPerMinute:     10,
	}, nil)
	defer hub.Close()

	// User connects twice (allowed)
	c1, err := hub.Register("user-1")
	assert.NoError(t, err)
	assert.NotNil(t, c1)

	c2, err := hub.Register("user-1")
	assert.NoError(t, err)
	assert.NotNil(t, c2)

	// Third connection exceeds max cap
	_, err = hub.Register("user-1")
	assert.ErrorContains(t, err, "max connections exceeded")

	// Other user can still connect
	c3, err := hub.Register("user-2")
	assert.NoError(t, err)
	assert.NotNil(t, c3)

	assert.Equal(t, 3, hub.ActiveConnections())
	assert.Equal(t, 2, hub.UserConnections("user-1"))
	assert.Equal(t, 1, hub.UserConnections("user-2"))

	// Unregistering c1 frees a slot for user-1
	_ = c1.Close()
	assert.Equal(t, 1, hub.UserConnections("user-1"))

	c4, err := hub.Register("user-1")
	assert.NoError(t, err)
	assert.NotNil(t, c4)
}

func TestTokenBucketRateLimiter(t *testing.T) {
	tb := websocket.NewTokenBucket(3) // 3 messages per minute
	assert.NotNil(t, tb)

	// Burst up to capacity (3 tokens)
	assert.True(t, tb.Allow())
	assert.True(t, tb.Allow())
	assert.True(t, tb.Allow())

	// Next call should be denied
	assert.False(t, tb.Allow())
}

func TestClientRateLimiterAndViolations(t *testing.T) {
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     2,
	}, nil)
	defer hub.Close()

	c, err := hub.Register("user-rate")
	require.NoError(t, err)
	defer c.Close()

	assert.NotEmpty(t, c.ID())
	assert.Equal(t, "user-rate", c.UserID())

	assert.True(t, c.AllowMessage())
	assert.True(t, c.AllowMessage())
	assert.False(t, c.AllowMessage())

	assert.Equal(t, int32(0), c.Violations())
	assert.Equal(t, int32(1), c.RecordViolation())
	assert.Equal(t, int32(2), c.RecordViolation())
	assert.Equal(t, int32(2), c.Violations())
}

func TestWebSocketBroadcast(t *testing.T) {
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     60,
	}, nil)
	defer hub.Close()

	u1c1, err := hub.Register("user-1")
	require.NoError(t, err)
	defer u1c1.Close()

	u1c2, err := hub.Register("user-1")
	require.NoError(t, err)
	defer u1c2.Close()

	u2c1, err := hub.Register("user-2")
	require.NoError(t, err)
	defer u2c1.Close()

	// 1. Broadcast to all clients
	err = hub.Broadcast([]byte("hello all"))
	assert.NoError(t, err)

	assert.Equal(t, []byte("hello all"), <-u1c1.SendChannel())
	assert.Equal(t, []byte("hello all"), <-u1c2.SendChannel())
	assert.Equal(t, []byte("hello all"), <-u2c1.SendChannel())

	// 2. Broadcast to specific user
	err = hub.BroadcastToUser("user-1", []byte("hello user 1"))
	assert.NoError(t, err)

	assert.Equal(t, []byte("hello user 1"), <-u1c1.SendChannel())
	assert.Equal(t, []byte("hello user 1"), <-u1c2.SendChannel())

	// u2 should receive nothing
	select {
	case msg := <-u2c1.SendChannel():
		t.Fatalf("unexpected message received by user-2: %s", string(msg))
	default:
		// expected
	}
}

func TestWebSocketSlowConsumerBufferDrop(t *testing.T) {
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     60,
	}, nil)
	defer hub.Close()

	c, err := hub.Register("slow-user")
	require.NoError(t, err)
	defer c.Close()

	// Fill the send buffer (256 items)
	for i := 0; i < 256; i++ {
		sent := c.Send([]byte("fill"))
		assert.True(t, sent)
	}

	// Buffer is now full; subsequent message should be dropped gracefully without blocking
	sent := c.Send([]byte("overflow"))
	assert.False(t, sent)
}

func TestWebSocketHubShutdown(t *testing.T) {
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     60,
	}, nil)

	c1, err := hub.Register("user-shutdown")
	require.NoError(t, err)

	assert.Equal(t, 1, hub.ActiveConnections())

	err = hub.Shutdown(context.Background())
	assert.NoError(t, err)

	// Hub is closed
	_, err = hub.Register("user-shutdown")
	assert.ErrorIs(t, err, websocket.ErrHubClosed)

	err = hub.Broadcast([]byte("test"))
	assert.ErrorIs(t, err, websocket.ErrHubClosed)

	// Client done channel is closed
	select {
	case <-c1.Done():
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("client done channel not closed on hub shutdown")
	}
}

func TestWebSocketHandshakeAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 2,
		MessagesPerMinute:     60,
		AllowedOrigins:        []string{"https://example.com"},
	}, nil)
	defer hub.Close()

	tm := &mockTokenManager{}
	handler := websocket.NewHandler(hub, tm, "https://example.com")

	router := gin.New()
	handler.RegisterRoutes(router, "/realtime")

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/realtime"

	// 1. Missing token -> 401
	resp, err := http.Get(server.URL + "/realtime")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// 2. Invalid token -> 401
	resp, err = http.Get(server.URL + "/realtime?token=bad-token")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// 3. Invalid Origin -> 403
	dialer := gorilla.Dialer{}
	header := http.Header{}
	header.Set("Origin", "https://malicious.com")
	_, resp, err = dialer.Dial(wsURL+"?token=valid-token", header)
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	}

	// 4. Valid token in query param & valid origin -> 101 Switching Protocols
	header = http.Header{}
	header.Set("Origin", "https://example.com")
	conn, resp, err := dialer.Dial(wsURL+"?token=valid-token", header)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer conn.Close()

	// Verify client is registered in hub
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, hub.UserConnections("user-test"))

	// 5. Valid token in Authorization header -> 101 Switching Protocols
	header = http.Header{}
	header.Set("Origin", "https://example.com")
	header.Set("Authorization", "Bearer valid-token")
	conn2, resp, err := dialer.Dial(wsURL, header)
	require.NoError(t, err)
	require.NotNil(t, conn2)
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer conn2.Close()

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 2, hub.UserConnections("user-test"))

	// 6. Third connection exceeds cap (MaxConnectionsPerUser: 2) -> closed
	header = http.Header{}
	header.Set("Origin", "https://example.com")
	conn3, resp, err := dialer.Dial(wsURL+"?token=valid-token", header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		// Server accepts HTTP upgrade then immediately sends close frame with ClosePolicyViolation
		_, _, readErr := conn3.ReadMessage()
		assert.Error(t, readErr)
		_ = conn3.Close()
	}
}

func TestWebSocketEndToEndMessageFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     120,
		PingPeriod:            100 * time.Millisecond,
		PongWait:              300 * time.Millisecond,
		WriteWait:             500 * time.Millisecond,
		AllowedOrigins:        []string{"*"},
	}, nil)
	defer hub.Close()

	var receivedMu sync.Mutex
	var receivedMsgs []string

	hub.SetMessageHandler(func(client *websocket.Client, message []byte) {
		receivedMu.Lock()
		receivedMsgs = append(receivedMsgs, string(message))
		receivedMu.Unlock()
	})

	handler := websocket.NewHandler(hub, &mockTokenManager{}, "*")

	router := gin.New()
	router.GET("/realtime", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/realtime"

	dialer := gorilla.Dialer{}
	header := http.Header{}
	header.Set("Authorization", "Bearer valid-token")
	conn, resp, err := dialer.Dial(wsURL, header)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Send message from client to server
	err = conn.WriteMessage(gorilla.TextMessage, []byte("ping from alice"))
	assert.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	receivedMu.Lock()
	assert.Contains(t, receivedMsgs, "ping from alice")
	receivedMu.Unlock()

	// Broadcast message from server to client
	err = hub.Broadcast([]byte("server announcement"))
	assert.NoError(t, err)

	msgType, p, err := conn.ReadMessage()
	assert.NoError(t, err)
	assert.Equal(t, gorilla.TextMessage, msgType)
	assert.Equal(t, "server announcement", string(p))
}

func TestWebSocketConfigAndHelpers(t *testing.T) {
	appCfg := config.WebSocketConfig{
		MaxConnectionsPerUser: 8,
		MessagesPerMinute:     200,
	}
	cfg := websocket.ConfigFromApp(appCfg)
	assert.Equal(t, 8, cfg.MaxConnectionsPerUser)
	assert.Equal(t, 200, cfg.MessagesPerMinute)

	hub := websocket.NewHub(cfg, nil)
	defer hub.Close()

	assert.NotEmpty(t, hub.NodeID())
	assert.Equal(t, 8, hub.Config().MaxConnectionsPerUser)

	handler := websocket.NewHandler(hub, nil)
	handler.SetUpgrader(gorilla.Upgrader{})
}

func TestInboundRateLimitDisconnect(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Tight rate limit: 1 message per minute
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     1,
		AllowedOrigins:        []string{"*"},
	}, nil)
	defer hub.Close()

	handler := websocket.NewHandler(hub, &mockTokenManager{}, "*")
	router := gin.New()
	router.GET("/realtime", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/realtime"

	header := http.Header{}
	header.Set("Authorization", "Bearer valid-token")
	conn, _, err := gorilla.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	defer conn.Close()

	// 1st message uses the 1 allowed token
	err = conn.WriteMessage(gorilla.TextMessage, []byte("msg 1"))
	require.NoError(t, err)

	// Send burst to trigger 5 violations and disconnect
	for i := 0; i < 7; i++ {
		_ = conn.WriteMessage(gorilla.TextMessage, []byte("spam"))
		time.Sleep(10 * time.Millisecond)
	}

	// Server should close the connection with ClosePolicyViolation
	_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	for {
		_, _, err = conn.ReadMessage()
		if err != nil {
			break
		}
	}
	assert.Error(t, err)
}

func handleMockRedisConn(c net.Conn, store map[string]int, subscribers map[string][]net.Conn, mu *sync.Mutex) {
	defer c.Close()
	reader := bufio.NewReader(c)
	isSubscribed := false
	var subscribedChannels []string

	defer func() {
		mu.Lock()
		for _, ch := range subscribedChannels {
			conns := subscribers[ch]
			var remaining []net.Conn
			for _, conn := range conns {
				if conn != c {
					remaining = append(remaining, conn)
				}
			}
			subscribers[ch] = remaining
		}
		mu.Unlock()
	}()

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "*") {
			continue
		}
		var numArgs int
		_, _ = fmt.Sscanf(line, "*%d", &numArgs)
		args := make([]string, 0, numArgs)
		for i := 0; i < numArgs; i++ {
			lenLine, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			var argLen int
			_, _ = fmt.Sscanf(lenLine, "$%d", &argLen)
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
		case "EVAL", "EVALSHA":
			mu.Lock()
			if len(args) >= 5 {
				// INCR script: key = args[3], maxConnections = args[4]
				key := args[3]
				maxConn, _ := strconv.Atoi(args[4])
				store[key]++
				if store[key] > maxConn {
					store[key]--
					_, _ = c.Write([]byte(":0\r\n"))
				} else {
					_, _ = c.Write([]byte(":1\r\n"))
				}
			} else if len(args) == 4 {
				// DECR script: key = args[3]
				key := args[3]
				store[key]--
				curr := store[key]
				if store[key] <= 0 {
					delete(store, key)
					curr = 0
				}
				_, _ = c.Write([]byte(fmt.Sprintf(":%d\r\n", curr)))
			} else {
				_, _ = c.Write([]byte("+OK\r\n"))
			}
			mu.Unlock()
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

func startMockRedis(t *testing.T) (*goredis.Client, string, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("skipping redis-dependent test: socket listen not permitted in environment")
		return nil, "", func() {}
	}

	addr := ln.Addr().String()
	store := make(map[string]int)
	subscribers := make(map[string][]net.Conn)
	var mu sync.Mutex
	closed := make(chan struct{})

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleMockRedisConn(conn, store, subscribers, &mu)
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

func TestRedisLuaSocketTracking(t *testing.T) {
	rdb, _, cleanup := startMockRedis(t)
	if rdb == nil {
		return
	}
	defer cleanup()

	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 2,
		MessagesPerMinute:     60,
	}, rdb)
	defer hub.Close()

	// 1. First connection for user-redis
	c1, err := hub.Register("user-redis")
	require.NoError(t, err)
	require.NotNil(t, c1)

	// 2. Second connection for user-redis
	c2, err := hub.Register("user-redis")
	require.NoError(t, err)
	require.NotNil(t, c2)

	// 3. Third connection should be rejected by Redis Lua capping
	_, err = hub.Register("user-redis")
	assert.ErrorIs(t, err, websocket.ErrMaxConnectionsExceeded)

	// 4. Closing one connection decrements Redis socket count
	_ = c1.Close()

	// 5. User can now connect again
	c3, err := hub.Register("user-redis")
	assert.NoError(t, err)
	assert.NotNil(t, c3)
	defer c3.Close()
	defer c2.Close()
}

func TestRedisHorizontalBroadcast(t *testing.T) {
	rdb1, addr, cleanup := startMockRedis(t)
	if rdb1 == nil {
		return
	}
	defer cleanup()

	rdb2 := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Protocol: 2,
	})
	defer rdb2.Close()

	hub1 := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     60,
		RedisChannel:          "test:cluster:broadcast",
	}, rdb1)
	defer hub1.Close()

	hub2 := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     60,
		RedisChannel:          "test:cluster:broadcast",
	}, rdb2)
	defer hub2.Close()

	// Allow Redis subscription goroutines to initialize
	time.Sleep(100 * time.Millisecond)

	c1, err := hub1.Register("alice")
	require.NoError(t, err)
	defer c1.Close()

	c2, err := hub2.Register("bob")
	require.NoError(t, err)
	defer c2.Close()

	// Hub 1 broadcasts cluster-wide -> both clients should receive it via Redis pubsub
	err = hub1.Broadcast([]byte("cluster announcement"))
	require.NoError(t, err)

	select {
	case msg := <-c1.SendChannel():
		assert.Equal(t, []byte("cluster announcement"), msg)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for broadcast message on hub 1 client")
	}

	select {
	case msg := <-c2.SendChannel():
		assert.Equal(t, []byte("cluster announcement"), msg)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for broadcast message on hub 2 client")
	}

	// Hub 2 broadcasts targeted to user "alice"
	err = hub2.BroadcastToUser("alice", []byte("direct message for alice"))
	require.NoError(t, err)

	select {
	case msg := <-c1.SendChannel():
		assert.Equal(t, []byte("direct message for alice"), msg)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for targeted message on hub 1 client")
	}

	// Bob should not receive alice's targeted message
	select {
	case msg := <-c2.SendChannel():
		t.Fatalf("unexpected targeted message received by bob: %s", string(msg))
	case <-time.After(100 * time.Millisecond):
		// Expected
	}
}

func TestAuthEmptyUserIDRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 2,
		MessagesPerMinute:     60,
	}, nil)
	defer hub.Close()

	tm := &mockTokenManager{
		validateFunc: func(ctx context.Context, token string) (*auth.Claims, error) {
			if token == "empty-user-token" {
				return &auth.Claims{UserID: ""}, nil
			}
			return nil, auth.ErrInvalidToken
		},
	}

	handler := websocket.NewHandler(hub, tm, "*")
	router := gin.New()
	handler.RegisterRoutes(router, "/realtime")

	server := httptest.NewServer(router)
	defer server.Close()

	// Token validates but has empty UserID -> MUST return 401 Unauthorized, NOT fall back to ?user_id=
	resp, err := http.Get(server.URL + "/realtime?token=empty-user-token&user_id=attacker")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestCheckOriginDefaultFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Hub with NO allowed origins configured
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 2,
		MessagesPerMinute:     60,
	}, nil)
	defer hub.Close()

	// Handler with empty allowedOrigins -> falls back to same-origin
	handler := websocket.NewHandler(hub, &mockTokenManager{})
	router := gin.New()
	handler.RegisterRoutes(router, "/realtime")

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/realtime"
	serverHost := strings.TrimPrefix(server.URL, "http://")

	// 1. Same-origin request (origin host == server host) -> Allowed (101)
	header := http.Header{}
	header.Set("Origin", "http://"+serverHost)
	header.Set("Authorization", "Bearer valid-token")
	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	_ = conn.Close()

	// 2. Cross-origin request from different host -> Rejected with 403 Forbidden
	header = http.Header{}
	header.Set("Origin", "http://malicious-site.com")
	header.Set("Authorization", "Bearer valid-token")
	_, resp, err = gorilla.DefaultDialer.Dial(wsURL, header)
	assert.Error(t, err)
	if resp != nil {
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	}
}

func TestRateLimiterViolationsDecayAndReset(t *testing.T) {
	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     10,
	}, nil)
	defer hub.Close()

	c, err := hub.Register("user-decay")
	require.NoError(t, err)
	defer c.Close()

	// Record violations
	assert.Equal(t, int32(1), c.RecordViolation())
	assert.Equal(t, int32(2), c.RecordViolation())
	assert.Equal(t, int32(3), c.RecordViolation())
	assert.Equal(t, int32(3), c.Violations())

	// Decay decreases by 1
	c.DecayViolations()
	assert.Equal(t, int32(2), c.Violations())

	c.DecayViolations()
	assert.Equal(t, int32(1), c.Violations())

	// Reset clears completely
	c.ResetViolations()
	assert.Equal(t, int32(0), c.Violations())
}

func TestWritePumpFrameIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     120,
		AllowedOrigins:        []string{"*"},
	}, nil)
	defer hub.Close()

	tm := &mockTokenManager{
		validateFunc: func(ctx context.Context, token string) (*auth.Claims, error) {
			if token == "alice-token" {
				return &auth.Claims{UserID: "alice"}, nil
			}
			return nil, auth.ErrInvalidToken
		},
	}
	handler := websocket.NewHandler(hub, tm, "*")
	router := gin.New()
	router.GET("/realtime", handler.HandleWebSocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/realtime"
	header := http.Header{}
	header.Set("Authorization", "Bearer alice-token")
	conn, resp, err := gorilla.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	defer conn.Close()

	time.Sleep(50 * time.Millisecond)

	// Send two messages back-to-back to client
	err = hub.BroadcastToUser("alice", []byte(`{"id":1}`))
	require.NoError(t, err)
	err = hub.BroadcastToUser("alice", []byte(`{"id":2}`))
	require.NoError(t, err)

	// Client should read each as a separate distinct frame, not concatenated with newline
	msgType1, payload1, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, gorilla.TextMessage, msgType1)
	assert.Equal(t, `{"id":1}`, string(payload1))

	msgType2, payload2, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Equal(t, gorilla.TextMessage, msgType2)
	assert.Equal(t, `{"id":2}`, string(payload2))
}

func TestWebSocket_RejectsRawQueryTokenInProduction(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub(websocket.Config{
		Env:                   "production",
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     60,
		AllowedOrigins:        []string{"https://example.com"},
	}, nil)
	defer hub.Close()

	tm := &mockTokenManager{}
	handler := websocket.NewHandler(hub, tm, "https://example.com")

	router := gin.New()
	handler.RegisterRoutes(router, "/realtime")

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/realtime"
	dialer := gorilla.Dialer{}

	// 1. ?token= query param MUST be rejected with 401 in production
	header := http.Header{}
	header.Set("Origin", "https://example.com")
	_, resp, err := dialer.Dial(wsURL+"?token=valid-token", header)
	assert.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_ = resp.Body.Close()

	// 2. ?token= query param WITH Authorization header is still rejected in production
	header = http.Header{}
	header.Set("Origin", "https://example.com")
	header.Set("Authorization", "Bearer valid-token")
	_, resp, err = dialer.Dial(wsURL+"?token=valid-token", header)
	assert.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	_ = resp.Body.Close()

	// 3. Authorization: Bearer header without query param succeeds in production
	header = http.Header{}
	header.Set("Origin", "https://example.com")
	header.Set("Authorization", "Bearer valid-token")
	conn, resp, err := dialer.Dial(wsURL, header)
	require.NoError(t, err)
	require.NotNil(t, conn)
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	_ = conn.Close()
}

func TestWebSocket_SecWebSocketProtocolAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub(websocket.Config{
		Env:                   "production",
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     60,
		AllowedOrigins:        []string{"https://example.com"},
	}, nil)
	defer hub.Close()

	tm := &mockTokenManager{}
	handler := websocket.NewHandler(hub, tm, "https://example.com")

	router := gin.New()
	handler.RegisterRoutes(router, "/realtime")

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/realtime"

	t.Run("bearer subprotocol pair succeeds and negotiates bearer", func(t *testing.T) {
		dialer := gorilla.Dialer{
			Subprotocols: []string{"bearer", "valid-token"},
		}
		header := http.Header{}
		header.Set("Origin", "https://example.com")
		conn, resp, err := dialer.Dial(wsURL, header)
		require.NoError(t, err)
		require.NotNil(t, conn)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		assert.Equal(t, "bearer", resp.Header.Get("Sec-WebSocket-Protocol"))
		_ = conn.Close()
	})

	t.Run("single token subprotocol succeeds", func(t *testing.T) {
		dialer := gorilla.Dialer{
			Subprotocols: []string{"valid-token"},
		}
		header := http.Header{}
		header.Set("Origin", "https://example.com")
		conn, resp, err := dialer.Dial(wsURL, header)
		require.NoError(t, err)
		require.NotNil(t, conn)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		_ = conn.Close()
	})

	t.Run("invalid subprotocol token fails with 401", func(t *testing.T) {
		dialer := gorilla.Dialer{
			Subprotocols: []string{"bearer", "bad-token"},
		}
		header := http.Header{}
		header.Set("Origin", "https://example.com")
		_, resp, err := dialer.Dial(wsURL, header)
		assert.Error(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		_ = resp.Body.Close()
	})
}

func TestWebSocket_FailsClosedWhenTokenManagerNil(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hub := websocket.NewHub(websocket.Config{
		MaxConnectionsPerUser: 5,
		MessagesPerMinute:     60,
		AllowedOrigins:        []string{"*"},
	}, nil)
	defer hub.Close()

	handler := websocket.NewHandler(hub, nil, "*")
	router := gin.New()
	handler.RegisterRoutes(router, "/realtime")

	server := httptest.NewServer(router)
	defer server.Close()

	// Fails closed even if ?user_id= is provided
	resp, err := http.Get(server.URL + "/realtime?user_id=attacker")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode)
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}
