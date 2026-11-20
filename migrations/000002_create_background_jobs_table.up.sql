CREATE TABLE IF NOT EXISTS background_jobs (
    id VARCHAR(36) PRIMARY KEY,
    queue VARCHAR(255) NOT NULL,
    payload BYTEA NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'pending',
    deduplication_key VARCHAR(255),
    claimed_by VARCHAR(255),
    lease_expires_at TIMESTAMPTZ,
    attempts INT NOT NULL DEFAULT 0,
    max_retries INT NOT NULL DEFAULT 3,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_background_jobs_queue_state_created_at 
    ON background_jobs (queue, state, created_at);

CREATE INDEX IF NOT EXISTS idx_background_jobs_state_lease_expires_at 
    ON background_jobs (state, lease_expires_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_background_jobs_active_dedup 
    ON background_jobs (queue, deduplication_key) 
    WHERE deduplication_key IS NOT NULL AND state IN ('pending', 'claimed');
