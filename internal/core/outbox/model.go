package outbox

import (
	"time"
)

// State represents the delivery lifecycle state of an outbox message.
type State string

const (
	// StatePending indicates the event has been stored transactionally and is waiting for relay.
	StatePending State = "pending"
	// StatePublished indicates the event was successfully delivered to downstream broker.
	StatePublished State = "published"
	// StateFailed indicates the event reached max retry attempts without successful delivery.
	StateFailed State = "failed"
)

// Event represents an outbox event record.
type Event struct {
	ID            string     `gorm:"primaryKey;type:uuid" json:"id"`
	AggregateType string     `gorm:"type:varchar(64);not null;index:idx_outbox_events_aggregate,priority:1" json:"aggregateType"`
	AggregateID   string     `gorm:"type:varchar(64);not null;index:idx_outbox_events_aggregate,priority:2" json:"aggregateId"`
	EventType     string     `gorm:"type:varchar(128);not null" json:"eventType"`
	Payload       []byte     `gorm:"type:bytea;not null" json:"payload"`
	Headers       []byte     `gorm:"type:jsonb" json:"headers,omitempty"`
	State         State      `gorm:"type:varchar(32);not null;default:'pending';index:idx_outbox_events_state_created_at,priority:1" json:"state"`
	RetryCount    int        `gorm:"default:0;not null" json:"retryCount"`
	LastError     *string    `gorm:"type:text" json:"lastError,omitempty"`
	CreatedAt     time.Time  `gorm:"not null;index:idx_outbox_events_state_created_at,priority:2" json:"createdAt"`
	PublishedAt   *time.Time `json:"publishedAt,omitempty"`
}

// TableName returns the PostgreSQL table name.
func (Event) TableName() string {
	return "outbox_events"
}
