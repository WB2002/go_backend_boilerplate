package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"

	"go_backend_boilerplate/internal/config"
)

var (
	// ErrMaxConnectionsExceeded indicates the user has exceeded their maximum concurrent connections.
	ErrMaxConnectionsExceeded = errors.New("max connections exceeded")
	// ErrHubClosed indicates the hub has already been shut down.
	ErrHubClosed = errors.New("websocket hub is closed")
	// ErrClientClosed indicates the client connection has closed.
	ErrClientClosed = errors.New("client connection closed")
)

const (
	defaultMaxConnectionsPerUser = 5
	defaultMessagesPerMinute     = 120
	defaultPingPeriod            = 54 * time.Second
	defaultPongWait              = 60 * time.Second
	defaultWriteWait             = 10 * time.Second
	defaultMaxMessageSize        = 512 * 1024 // 512 KB
	defaultRedisChannel          = "realtime:broadcast"
	redisSocketKeyTTL            = 24 * time.Hour
)

// Config configures the WebSocket hub and connection lifecycle.
type Config struct {
	MaxConnectionsPerUser int           `json:"max_connections_per_user"`
	MessagesPerMinute     int           `json:"messages_per_minute"`
	AllowedOrigins        []string      `json:"allowed_origins"`
	PingPeriod            time.Duration `json:"ping_period"`
	PongWait              time.Duration `json:"pong_wait"`
	WriteWait             time.Duration `json:"write_wait"`
	MaxMessageSize        int64         `json:"max_message_size"`
	RedisChannel          string        `json:"redis_channel"`
}

// ConfigFromApp maps application WebSocketConfig to hub Config.
func ConfigFromApp(appCfg config.WebSocketConfig) Config {
	return Config{
		MaxConnectionsPerUser: appCfg.MaxConnectionsPerUser,
		MessagesPerMinute:     appCfg.MessagesPerMinute,
	}
}

// BroadcastEnvelope formats cross-node and local broadcast payloads.
type BroadcastEnvelope struct {
	NodeID    string `json:"node_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Payload   []byte `json:"payload"`
	CreatedAt int64  `json:"created_at"`
}

// Hub manages active WebSocket clients, multi-node horizontal broadcasts,
// and per-user connection limits.
type Hub struct {
	mu          sync.RWMutex
	config      Config
	redisClient *goredis.Client
	nodeID      string

	// Local clients
	clients     map[*Client]bool
	userClients map[string]map[*Client]bool

	// Cross-node coordination
	pubsub *goredis.PubSub
	ctx    context.Context
	cancel context.CancelFunc
	closed bool

	// Optional inbound message handler
	onMessage func(client *Client, message []byte)
}

// NewHub creates and initializes a new WebSocket Hub.
func NewHub(cfg Config, redisClient *goredis.Client) *Hub {
	if cfg.MaxConnectionsPerUser <= 0 {
		cfg.MaxConnectionsPerUser = defaultMaxConnectionsPerUser
	}
	if cfg.MessagesPerMinute <= 0 {
		cfg.MessagesPerMinute = defaultMessagesPerMinute
	}
	if cfg.PingPeriod <= 0 {
		cfg.PingPeriod = defaultPingPeriod
	}
	if cfg.PongWait <= 0 {
		cfg.PongWait = defaultPongWait
	}
	if cfg.WriteWait <= 0 {
		cfg.WriteWait = defaultWriteWait
	}
	if cfg.MaxMessageSize <= 0 {
		cfg.MaxMessageSize = defaultMaxMessageSize
	}
	if cfg.RedisChannel == "" {
		cfg.RedisChannel = defaultRedisChannel
	}

	ctx, cancel := context.WithCancel(context.Background())

	h := &Hub{
		config:      cfg,
		redisClient: redisClient,
		nodeID:      uuid.NewString(),
		clients:     make(map[*Client]bool),
		userClients: make(map[string]map[*Client]bool),
		ctx:         ctx,
		cancel:      cancel,
	}

	if redisClient != nil {
		h.pubsub = redisClient.Subscribe(ctx, cfg.RedisChannel)
		go h.listenRedis()
	}

	return h
}

// SetMessageHandler configures a callback for incoming messages received by clients.
func (h *Hub) SetMessageHandler(fn func(client *Client, message []byte)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onMessage = fn
}

// NodeID returns the unique instance identifier of this hub.
func (h *Hub) NodeID() string {
	return h.nodeID
}

// Config returns the active hub configuration.
func (h *Hub) Config() Config {
	return h.config
}

// Register adds a client for a given userID, enforcing per-user connection caps.
func (h *Hub) Register(userID string, conn ...*websocket.Conn) (*Client, error) {
	if userID == "" {
		userID = "anonymous"
	}

	// 1. In-memory connection limit pre-check under RLock
	h.mu.RLock()
	if h.closed {
		h.mu.RUnlock()
		return nil, ErrHubClosed
	}
	existingCount := len(h.userClients[userID])
	if existingCount >= h.config.MaxConnectionsPerUser {
		h.mu.RUnlock()
		return nil, ErrMaxConnectionsExceeded
	}
	h.mu.RUnlock()

	// 2. Redis distributed connection limit check outside the mutex lock
	var redisIncremented bool
	if h.redisClient != nil {
		allowed, err := h.incrRedisUserSockets(userID, h.config.MaxConnectionsPerUser)
		if err != nil {
			log.Warn().Err(err).Str("user_id", userID).Msg("redis socket counter failed, falling back to local memory")
		} else if !allowed {
			return nil, ErrMaxConnectionsExceeded
		} else {
			redisIncremented = true
		}
	}

	// 3. Acquire exclusive lock briefly to insert client into map
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		if redisIncremented {
			_ = h.decrRedisUserSockets(userID)
		}
		return nil, ErrHubClosed
	}

	if len(h.userClients[userID]) >= h.config.MaxConnectionsPerUser {
		h.mu.Unlock()
		if redisIncremented {
			_ = h.decrRedisUserSockets(userID)
		}
		return nil, ErrMaxConnectionsExceeded
	}

	var wsConn *websocket.Conn
	if len(conn) > 0 {
		wsConn = conn[0]
	}

	client := newClient(h, userID, wsConn)
	h.clients[client] = true

	if h.userClients[userID] == nil {
		h.userClients[userID] = make(map[*Client]bool)
	}
	h.userClients[userID][client] = true
	h.mu.Unlock()

	return client, nil
}

// Unregister removes a client, updating in-memory counts and decrementing Redis tracking.
func (h *Hub) Unregister(client *Client) {
	if client == nil {
		return
	}

	h.mu.Lock()
	if !h.clients[client] {
		h.mu.Unlock()
		return
	}

	delete(h.clients, client)

	userID := client.UserID()
	if userGroup, ok := h.userClients[userID]; ok {
		delete(userGroup, client)
		if len(userGroup) == 0 {
			delete(h.userClients, userID)
		}
	}
	h.mu.Unlock()

	// Execute Redis decrement outside the mutex lock
	if h.redisClient != nil {
		if err := h.decrRedisUserSockets(userID); err != nil {
			log.Warn().Err(err).Str("user_id", userID).Msg("failed to decrement redis socket count")
		}
	}
}

// Broadcast sends a message to all connected clients across all nodes via Redis Pub/Sub
// or delivers locally when Redis is unavailable.
func (h *Hub) Broadcast(payload []byte) error {
	envelope := BroadcastEnvelope{
		NodeID:    h.nodeID,
		Payload:   payload,
		CreatedAt: time.Now().Unix(),
	}

	return h.publishOrDispatch(envelope)
}

// BroadcastToUser sends a message to all connected clients of a specific user.
func (h *Hub) BroadcastToUser(userID string, payload []byte) error {
	envelope := BroadcastEnvelope{
		NodeID:    h.nodeID,
		UserID:    userID,
		Payload:   payload,
		CreatedAt: time.Now().Unix(),
	}

	return h.publishOrDispatch(envelope)
}

// publishOrDispatch publishes to Redis if connected, otherwise falls back to local dispatch.
func (h *Hub) publishOrDispatch(envelope BroadcastEnvelope) error {
	h.mu.RLock()
	isClosed := h.closed
	h.mu.RUnlock()

	if isClosed {
		return ErrHubClosed
	}

	if h.redisClient != nil {
		data, err := json.Marshal(envelope)
		if err != nil {
			return fmt.Errorf("failed to marshal broadcast envelope: %w", err)
		}

		ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
		defer cancel()

		if err := h.redisClient.Publish(ctx, h.config.RedisChannel, data).Err(); err != nil {
			log.Warn().Err(err).Msg("redis publish failed, delivering locally")
			h.dispatchLocal(envelope)
			return err
		}
		return nil
	}

	// Local-only delivery
	h.dispatchLocal(envelope)
	return nil
}

// dispatchLocal delivers the envelope to matching locally registered clients.
func (h *Hub) dispatchLocal(envelope BroadcastEnvelope) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.closed {
		return
	}

	if envelope.UserID != "" {
		if clients, ok := h.userClients[envelope.UserID]; ok {
			for c := range clients {
				c.Send(envelope.Payload)
			}
		}
		return
	}

	for c := range h.clients {
		c.Send(envelope.Payload)
	}
}

// listenRedis subscribes to cross-node broadcast events from Redis Pub/Sub.
func (h *Hub) listenRedis() {
	ch := h.pubsub.Channel()
	for {
		select {
		case <-h.ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var envelope BroadcastEnvelope
			if err := json.Unmarshal([]byte(msg.Payload), &envelope); err != nil {
				log.Warn().Err(err).Msg("failed to unmarshal redis broadcast message")
				continue
			}
			h.dispatchLocal(envelope)
		}
	}
}

// ActiveConnections returns the total number of clients connected to this node.
func (h *Hub) ActiveConnections() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// UserConnections returns the number of active clients for a user on this node.
func (h *Hub) UserConnections(userID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.userClients[userID])
}

// Run runs the hub until the provided context is canceled, then shuts down.
func (h *Hub) Run(ctx context.Context) {
	<-ctx.Done()
	_ = h.Close()
}

// Close gracefully shuts down the hub and disconnects all local clients.
func (h *Hub) Close() error {
	return h.Shutdown(context.Background())
}

// Shutdown stops the Redis listener, closes all active clients, and frees resources.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.cancel()

	// Close Redis Pub/Sub
	if h.pubsub != nil {
		_ = h.pubsub.Close()
	}

	// Snapshot clients to close
	clientsToClose := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clientsToClose = append(clientsToClose, c)
	}
	h.mu.Unlock()

	// Close each client
	for _, c := range clientsToClose {
		_ = c.Close()
	}

	return nil
}

// Redis Lua scripts for atomic socket limit enforcement
var incrSocketScript = goredis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
    redis.call('EXPIRE', KEYS[1], ARGV[2])
end
if tonumber(current) > tonumber(ARGV[1]) then
    redis.call('DECR', KEYS[1])
    return 0
end
return 1
`)

var decrSocketScript = goredis.NewScript(`
local current = redis.call('DECR', KEYS[1])
if current <= 0 then
    redis.call('DEL', KEYS[1])
end
return current
`)

func (h *Hub) redisUserSocketKey(userID string) string {
	return fmt.Sprintf("user:sockets:%s", userID)
}

func (h *Hub) incrRedisUserSockets(userID string, maxConnections int) (bool, error) {
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
	defer cancel()

	key := h.redisUserSocketKey(userID)
	res, err := incrSocketScript.Run(ctx, h.redisClient, []string{key}, maxConnections, int(redisSocketKeyTTL.Seconds())).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (h *Hub) decrRedisUserSockets(userID string) error {
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
	defer cancel()

	key := h.redisUserSocketKey(userID)
	return decrSocketScript.Run(ctx, h.redisClient, []string{key}).Err()
}
