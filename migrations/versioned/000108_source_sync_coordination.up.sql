-- Durable source trigger coordination. Queue IDs deduplicate delivery only;
-- these rows are the authority for per-source ordering and worker fencing.
ALTER TABLE sync_logs
    ADD COLUMN IF NOT EXISTS source_config_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS source_fencing_token BIGINT NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS source_sync_states (
    data_source_id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    config_generation BIGINT NOT NULL DEFAULT 1,
    config_fingerprint TEXT NOT NULL,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    lease_owner VARCHAR(36),
    lease_expires_at TIMESTAMPTZ,
    active_sync_log_id VARCHAR(36),
    pending_sync_log_id VARCHAR(36),
    pending_delivery_generation BIGINT NOT NULL DEFAULT 0,
    pending_trigger TEXT NOT NULL DEFAULT '',
    last_successful_snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
    last_successful_published_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS source_sync_states_pending
    ON source_sync_states(updated_at) WHERE pending_sync_log_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS source_sync_states_expired_lease
    ON source_sync_states(lease_expires_at) WHERE active_sync_log_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS source_sync_runs (
    sync_log_id VARCHAR(36) PRIMARY KEY,
    data_source_id VARCHAR(36) NOT NULL,
    tenant_id BIGINT NOT NULL,
    config_generation BIGINT NOT NULL,
    fencing_token BIGINT NOT NULL DEFAULT 0,
    delivery_generation BIGINT NOT NULL DEFAULT 0,
    trigger TEXT NOT NULL,
    phase TEXT NOT NULL DEFAULT 'queued',
    target_commit_sha TEXT NOT NULL DEFAULT '',
    completed_stages JSONB NOT NULL DEFAULT '[]'::jsonb,
    budget_calls INTEGER NOT NULL DEFAULT 0,
    budget_tokens BIGINT NOT NULL DEFAULT 0,
    budget_repairs INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS source_sync_runs_source
    ON source_sync_runs(data_source_id, updated_at DESC);

-- The source publication pointer and this pending Wiki signal are committed
-- together. A dispatcher can safely retry pending rows after process restart.
CREATE TABLE IF NOT EXISTS source_publication_outbox (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    data_source_id VARCHAR(36) NOT NULL,
    snapshot_id VARCHAR(36) NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    UNIQUE(data_source_id, snapshot_id, event_type)
);
CREATE INDEX IF NOT EXISTS source_publication_outbox_pending
    ON source_publication_outbox(created_at) WHERE status='pending';
