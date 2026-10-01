-- Optional GitLab Push Hook authentication and durable delivery deduplication.
-- Hook receipts and their source-sync trigger are committed in one transaction.
CREATE TABLE IF NOT EXISTS source_gitlab_webhook_configs (
    data_source_id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    secret_ciphertext TEXT NOT NULL DEFAULT '',
    last_received_at TIMESTAMPTZ,
    last_event_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS source_gitlab_webhook_deliveries (
    data_source_id VARCHAR(36) NOT NULL,
    tenant_id BIGINT NOT NULL,
    delivery_id TEXT NOT NULL,
    event_id TEXT NOT NULL DEFAULT '',
    ref TEXT NOT NULL,
    before_sha TEXT NOT NULL,
    after_sha TEXT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (data_source_id, delivery_id)
);

CREATE INDEX IF NOT EXISTS source_gitlab_webhook_deliveries_received
    ON source_gitlab_webhook_deliveries(received_at DESC);
