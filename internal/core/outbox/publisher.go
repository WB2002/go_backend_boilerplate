package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// Publisher defines the contract for dispatching outbox events to downstream consumers or messaging brokers.
type Publisher interface {
	Publish(ctx context.Context, event *Event) error
}

// RedisPublisher publishes outbox events to Redis Pub/Sub channels.
type RedisPublisher struct {
	client goredis.Cmdable
}

// NewRedisPublisher constructs a new RedisPublisher.
func NewRedisPublisher(client goredis.Cmdable) *RedisPublisher {
	return &RedisPublisher{
		client: client,
	}
}

// Publish serializes and publishes the outbox event to Redis Pub/Sub channel "outbox:events:<event_type>".
func (p *RedisPublisher) Publish(ctx context.Context, event *Event) error {
	if p.client == nil {
		return nil
	}

	channel := fmt.Sprintf("outbox:events:%s", event.EventType)
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal outbox event: %w", err)
	}

	return p.client.Publish(ctx, channel, payload).Err()
}
