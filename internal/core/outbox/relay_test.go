package outbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockPublisher struct {
	mu        sync.Mutex
	published []*Event
}

func (m *mockPublisher) Publish(ctx context.Context, event *Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, event)
	return nil
}

func TestOutboxRelayProcessing(t *testing.T) {
	db := setupTestDB(t)

	store := NewStore(db)
	pub := &mockPublisher{}

	// Insert 2 pending events
	ctx := context.Background()
	_ = store.CreateEvent(ctx, db, &Event{
		ID:            uuid.NewString(),
		AggregateType: "order",
		AggregateID:   "ord-1",
		EventType:     "order.paid",
		Payload:       []byte(`{"total": 50}`),
		State:         StatePending,
		CreatedAt:     time.Now(),
	})
	_ = store.CreateEvent(ctx, db, &Event{
		ID:            uuid.NewString(),
		AggregateType: "order",
		AggregateID:   "ord-2",
		EventType:     "order.shipped",
		Payload:       []byte(`{"carrier": "fedex"}`),
		State:         StatePending,
		CreatedAt:     time.Now(),
	})

	relay := NewRelay(db, pub, RelayConfig{
		BatchSize:    10,
		PollInterval: 10 * time.Millisecond,
		MaxRetries:   3,
	})

	relayCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	_ = relay.Start(relayCtx)

	// Verify both published
	pub.mu.Lock()
	assert.Len(t, pub.published, 2)
	pub.mu.Unlock()

	// Verify state in DB transitioned to published
	var publishedCount int64
	db.Model(&Event{}).Where("state = ?", StatePublished).Count(&publishedCount)
	assert.Equal(t, int64(2), publishedCount)
}

type failingPublisher struct {
	err error
}

func (f *failingPublisher) Publish(ctx context.Context, event *Event) error {
	return f.err
}

func TestOutboxRelayRetryAndFailure(t *testing.T) {
	db := setupTestDB(t)

	store := NewStore(db)
	pub := &failingPublisher{err: assert.AnError}

	ctx := context.Background()
	evt := &Event{
		ID:            uuid.NewString(),
		AggregateType: "payment",
		AggregateID:   "pay-1",
		EventType:     "payment.failed",
		Payload:       []byte(`{}`),
		State:         StatePending,
		CreatedAt:     time.Now(),
	}
	require.NoError(t, store.CreateEvent(ctx, db, evt))

	relay := NewRelay(db, pub, RelayConfig{
		BatchSize:    1,
		PollInterval: 10 * time.Millisecond,
		MaxRetries:   2,
	})

	// First attempt
	n, err := relay.ProcessBatch(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 1, n)

	var record Event
	require.NoError(t, db.First(&record, "id = ?", evt.ID).Error)
	assert.Equal(t, StatePending, record.State)
	assert.Equal(t, 1, record.RetryCount)
	assert.NotNil(t, record.LastError)

	// Second attempt: exceeds or equals max retries -> StateFailed
	n, err = relay.ProcessBatch(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 1, n)

	require.NoError(t, db.First(&record, "id = ?", evt.ID).Error)
	assert.Equal(t, StateFailed, record.State)
	assert.Equal(t, 2, record.RetryCount)
}

func TestRedisPublisherNilClient(t *testing.T) {
	pub := NewRedisPublisher(nil)
	err := pub.Publish(context.Background(), &Event{EventType: "test"})
	assert.NoError(t, err)
}
