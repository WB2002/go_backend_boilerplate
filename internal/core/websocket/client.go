package websocket

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

const (
	sendBufferSize = 256
	maxViolations  = 5
)

// TokenBucket implements a token bucket rate limiter for inbound socket frames.
type TokenBucket struct {
	mu           sync.Mutex
	capacity     float64
	tokens       float64
	refillRate   float64 // tokens per second
	lastRefilled time.Time
}

// NewTokenBucket creates a token bucket configured with a rate per minute.
func NewTokenBucket(messagesPerMinute int) *TokenBucket {
	if messagesPerMinute <= 0 {
		messagesPerMinute = defaultMessagesPerMinute
	}
	capacity := float64(messagesPerMinute)
	return &TokenBucket{
		capacity:     capacity,
		tokens:       capacity,
		refillRate:   capacity / 60.0,
		lastRefilled: time.Now(),
	}
}

// Allow reports whether a single token can be consumed.
func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefilled).Seconds()
	tb.lastRefilled = now

	// Refill tokens
	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}

	return false
}

// IsFull reports whether the token bucket has refilled to full capacity.
func (tb *TokenBucket) IsFull() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefilled).Seconds()
	tokens := tb.tokens + elapsed*tb.refillRate
	return tokens >= tb.capacity
}

// Client represents a connected WebSocket client instance.
type Client struct {
	id         string
	userID     string
	hub        *Hub
	conn       *websocket.Conn
	send       chan []byte
	done       chan struct{}
	closeOnce  sync.Once
	limiter    *TokenBucket
	violations int32
	closed     atomic.Bool
}

func newClient(hub *Hub, userID string, conn *websocket.Conn) *Client {
	return &Client{
		id:      uuid.NewString(),
		userID:  userID,
		hub:     hub,
		conn:    conn,
		send:    make(chan []byte, sendBufferSize),
		done:    make(chan struct{}),
		limiter: NewTokenBucket(hub.config.MessagesPerMinute),
	}
}

// ID returns the client's unique connection ID.
func (c *Client) ID() string {
	return c.id
}

// UserID returns the associated user's ID.
func (c *Client) UserID() string {
	return c.userID
}

// Send queues a message payload to be written to the WebSocket.
// If the buffer is full or the client is closed, the message is dropped.
func (c *Client) Send(payload []byte) bool {
	if c.closed.Load() {
		return false
	}

	select {
	case c.send <- payload:
		return true
	default:
		// Drop message if outbound buffer full to protect server memory
		log.Warn().Str("client_id", c.id).Str("user_id", c.userID).Msg("websocket send buffer full, dropping message")
		return false
	}
}

// SendChannel exposes the outbound send channel (for custom consumers or tests).
func (c *Client) SendChannel() <-chan []byte {
	return c.send
}

// Done returns a channel that is closed when the client is disconnected.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// AllowMessage checks whether the client's inbound rate limiter permits a new message.
func (c *Client) AllowMessage() bool {
	return c.limiter.Allow()
}

// RecordViolation increments the rate limit violation counter.
func (c *Client) RecordViolation() int32 {
	return atomic.AddInt32(&c.violations, 1)
}

// Violations returns the current violation count.
func (c *Client) Violations() int32 {
	return atomic.LoadInt32(&c.violations)
}

// ResetViolations clears the rate limit violation counter to zero.
func (c *Client) ResetViolations() {
	atomic.StoreInt32(&c.violations, 0)
}

// DecayViolations decrements the violation count by one (down to zero).
func (c *Client) DecayViolations() {
	for {
		v := atomic.LoadInt32(&c.violations)
		if v <= 0 {
			return
		}
		if atomic.CompareAndSwapInt32(&c.violations, v, v-1) {
			return
		}
	}
}

// Close disconnects the client and cleans up hub registrations.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)

		// Unregister from hub
		c.hub.Unregister(c)

		// Close physical connection if present
		if c.conn != nil {
			err = c.conn.Close()
		}
	})
	return err
}

// ReadPump reads incoming messages from the WebSocket connection, applying
// rate limiting and dispatching messages until the connection is closed.
func (c *Client) ReadPump() {
	defer func() {
		_ = c.Close()
	}()

	if c.conn == nil {
		return
	}

	cfg := c.hub.config
	c.conn.SetReadLimit(cfg.MaxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(cfg.PongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(cfg.PongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Debug().Err(err).Str("client_id", c.id).Msg("websocket read error")
			}
			break
		}

		// Enforce inbound message rate limiter
		if !c.AllowMessage() {
			v := c.RecordViolation()
			log.Warn().
				Str("client_id", c.id).
				Str("user_id", c.userID).
				Int32("violations", v).
				Msg("websocket inbound rate limit exceeded")

			if v >= maxViolations {
				// Severe violation: terminate connection
				_ = c.conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "rate limit exceeded"),
					time.Now().Add(cfg.WriteWait),
				)
				break
			}
			// Drop packet
			continue
		}

		// Compliant message: decay or reset accumulated violations
		if c.Violations() > 0 {
			if c.limiter.IsFull() {
				c.ResetViolations()
			} else {
				c.DecayViolations()
			}
		}

		// Dispatch message to hub handler if registered
		c.hub.mu.RLock()
		handler := c.hub.onMessage
		c.hub.mu.RUnlock()

		if handler != nil {
			handler(c, message)
		}
	}
}

// WritePump handles outbound message queuing and ping/pong keep-alives.
func (c *Client) WritePump() {
	if c.conn == nil {
		return
	}

	cfg := c.hub.config
	ticker := time.NewTicker(cfg.PingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.Close()
	}()

	for {
		select {
		case <-c.done:
			return

		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(cfg.WriteWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// Deliver each message as a discrete WebSocket frame, preserving RFC 6455 framing
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			if _, err := w.Write(message); err != nil {
				_ = w.Close()
				return
			}
			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(cfg.WriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
