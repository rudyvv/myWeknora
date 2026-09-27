CREATE TABLE wecom_cli_connections (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    name VARCHAR(128) NOT NULL,
    credentials JSONB NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    status VARCHAR(32) NOT NULL DEFAULT 'unknown',
    last_error VARCHAR(255) NOT NULL DEFAULT '',
    last_check_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX idx_wecom_cli_connections_tenant ON wecom_cli_connections (tenant_id);
CREATE UNIQUE INDEX idx_wecom_cli_connections_default
    ON wecom_cli_connections (tenant_id) WHERE is_default AND deleted_at IS NULL;

CREATE TABLE wedrive_devices (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    name VARCHAR(128) NOT NULL,
    public_key TEXT NOT NULL,
    agent_version VARCHAR(32) NOT NULL DEFAULT '',
    platform VARCHAR(32) NOT NULL DEFAULT 'windows',
    status VARCHAR(32) NOT NULL DEFAULT 'offline',
    last_seen_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX idx_wedrive_devices_tenant_user ON wedrive_devices (tenant_id, user_id);

CREATE TABLE wedrive_device_registrations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    code_hash VARCHAR(64) NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_wedrive_device_registrations_expiry ON wedrive_device_registrations (expires_at);

CREATE TABLE wedrive_sources (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    data_source_id VARCHAR(36) NOT NULL DEFAULT '',
    connection_id VARCHAR(36) NOT NULL DEFAULT '',
    device_id VARCHAR(36) NOT NULL,
    created_by VARCHAR(36) NOT NULL,
    approved_by VARCHAR(36) NOT NULL DEFAULT '',
    name VARCHAR(128) NOT NULL,
    root_url TEXT NOT NULL,
    root_external_id VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    auto_share BOOLEAN NOT NULL DEFAULT TRUE,
    sync_deletions BOOLEAN NOT NULL DEFAULT FALSE,
    sync_schedule VARCHAR(64) NOT NULL DEFAULT '',
    last_snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
    last_complete_scan_at TIMESTAMPTZ,
    approved_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX idx_wedrive_sources_tenant_status ON wedrive_sources (tenant_id, status);
CREATE INDEX idx_wedrive_sources_kb ON wedrive_sources (knowledge_base_id);
CREATE INDEX idx_wedrive_sources_device ON wedrive_sources (device_id);

CREATE TABLE wedrive_snapshots (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    source_id VARCHAR(36) NOT NULL,
    device_id VARCHAR(36) NOT NULL,
    sequence BIGINT NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'uploading',
    expected_item_count INTEGER NOT NULL DEFAULT 0,
    received_item_count INTEGER NOT NULL DEFAULT 0,
    digest VARCHAR(64) NOT NULL DEFAULT '',
    auto_share_existing INTEGER NOT NULL DEFAULT 0,
    auto_share_created INTEGER NOT NULL DEFAULT 0,
    auto_share_failed INTEGER NOT NULL DEFAULT 0,
    committed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (source_id, sequence)
);
CREATE INDEX idx_wedrive_snapshots_source_created ON wedrive_snapshots (source_id, created_at DESC);

CREATE TABLE wedrive_inventory_items (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    source_id VARCHAR(36) NOT NULL,
    snapshot_id VARCHAR(36) NOT NULL,
    external_id VARCHAR(512) NOT NULL,
    parent_external_id VARCHAR(512) NOT NULL DEFAULT '',
    name VARCHAR(512) NOT NULL,
    path TEXT NOT NULL,
    item_type VARCHAR(64) NOT NULL,
    doc_id VARCHAR(255) NOT NULL DEFAULT '',
    share_url TEXT NOT NULL DEFAULT '',
    mime_type VARCHAR(128) NOT NULL DEFAULT '',
    size BIGINT NOT NULL DEFAULT 0,
    modified_at TIMESTAMPTZ,
    content_fingerprint VARCHAR(128) NOT NULL DEFAULT '',
    consumability VARCHAR(64) NOT NULL DEFAULT 'unknown',
    failure_code VARCHAR(64) NOT NULL DEFAULT '',
    missing_count INTEGER NOT NULL DEFAULT 0,
    missing_since TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (snapshot_id, external_id)
);
CREATE INDEX idx_wedrive_items_source_snapshot ON wedrive_inventory_items (source_id, snapshot_id);
