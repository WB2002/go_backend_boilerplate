package audit

import (
	"time"
)

// LogRecord represents the persisted audit log entry in PostgreSQL.
type LogRecord struct {
	ID           string    `gorm:"primaryKey;type:uuid" json:"id"`
	ActorID      string    `gorm:"type:varchar(64);not null;index:idx_audit_logs_actor_created_at,priority:1" json:"actorId"`
	ActorEmail   string    `gorm:"type:varchar(255)" json:"actorEmail,omitempty"`
	Action       string    `gorm:"type:varchar(128);not null;index:idx_audit_logs_action_created_at,priority:1" json:"action"`
	TargetType   string    `gorm:"type:varchar(64);not null;index:idx_audit_logs_target,priority:1" json:"targetType"`
	TargetID     string    `gorm:"type:varchar(64);not null;index:idx_audit_logs_target,priority:2" json:"targetId"`
	IPAddress    string    `gorm:"type:varchar(45)" json:"ipAddress,omitempty"`
	UserAgent    string    `gorm:"type:text" json:"userAgent,omitempty"`
	Changes      []byte    `gorm:"type:jsonb" json:"changes,omitempty"`
	Status       string    `gorm:"type:varchar(32);not null;default:'success'" json:"status"`
	ErrorMessage string    `gorm:"type:text" json:"errorMessage,omitempty"`
	CreatedAt    time.Time `gorm:"not null;index:idx_audit_logs_action_created_at,priority:2;index:idx_audit_logs_actor_created_at,priority:2" json:"createdAt"`
}

// TableName returns the table name for PostgreSQL.
func (LogRecord) TableName() string {
	return "audit_logs"
}

// Entry is the input payload for recording an audit log.
type Entry struct {
	ActorID      string
	ActorEmail   string
	Action       string
	TargetType   string
	TargetID     string
	IPAddress    string
	UserAgent    string
	Changes      map[string]any
	Status       string
	ErrorMessage string
}
