CREATE TABLE IF NOT EXISTS audit_logs (
    id UUID PRIMARY KEY,
    actor_id VARCHAR(64) NOT NULL,
    actor_email VARCHAR(255),
    action VARCHAR(128) NOT NULL,
    target_type VARCHAR(64) NOT NULL,
    target_id VARCHAR(64) NOT NULL,
    ip_address VARCHAR(45),
    user_agent TEXT,
    changes JSONB,
    status VARCHAR(32) NOT NULL DEFAULT 'success',
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_logs_action_created_at ON audit_logs (action, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor_created_at ON audit_logs (actor_id, created_at);
CREATE INDEX IF NOT EXISTS idx_audit_logs_target ON audit_logs (target_type, target_id);
