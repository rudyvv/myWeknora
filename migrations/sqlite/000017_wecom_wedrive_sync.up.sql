CREATE TABLE wecom_cli_connections (
    id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, name TEXT NOT NULL,
    credentials TEXT NOT NULL, is_default INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'unknown', last_error TEXT NOT NULL DEFAULT '',
    last_check_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
);
CREATE INDEX idx_wecom_cli_connections_tenant ON wecom_cli_connections (tenant_id);
CREATE UNIQUE INDEX idx_wecom_cli_connections_default ON wecom_cli_connections (tenant_id)
    WHERE is_default = 1 AND deleted_at IS NULL;

CREATE TABLE wedrive_devices (
    id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, user_id TEXT NOT NULL,
    name TEXT NOT NULL, public_key TEXT NOT NULL, agent_version TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT 'windows', status TEXT NOT NULL DEFAULT 'offline',
    last_seen_at DATETIME, revoked_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
);
CREATE INDEX idx_wedrive_devices_tenant_user ON wedrive_devices (tenant_id, user_id);

CREATE TABLE wedrive_device_registrations (
    id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, user_id TEXT NOT NULL,
    code_hash TEXT NOT NULL UNIQUE, expires_at DATETIME NOT NULL, used_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_wedrive_device_registrations_expiry ON wedrive_device_registrations (expires_at);

CREATE TABLE wedrive_sources (
    id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, knowledge_base_id TEXT NOT NULL,
    data_source_id TEXT NOT NULL DEFAULT '', connection_id TEXT NOT NULL DEFAULT '',
    device_id TEXT NOT NULL, created_by TEXT NOT NULL, approved_by TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL, root_url TEXT NOT NULL, root_external_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending', auto_share INTEGER NOT NULL DEFAULT 1,
    sync_deletions INTEGER NOT NULL DEFAULT 0, sync_schedule TEXT NOT NULL DEFAULT '',
    last_snapshot_id TEXT NOT NULL DEFAULT '', last_complete_scan_at DATETIME, approved_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
CREATE INDEX idx_wedrive_sources_tenant_status ON wedrive_sources (tenant_id, status);
CREATE INDEX idx_wedrive_sources_kb ON wedrive_sources (knowledge_base_id);
CREATE INDEX idx_wedrive_sources_device ON wedrive_sources (device_id);

CREATE TABLE wedrive_snapshots (
    id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, source_id TEXT NOT NULL,
    device_id TEXT NOT NULL, sequence INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'uploading',
    expected_item_count INTEGER NOT NULL DEFAULT 0, received_item_count INTEGER NOT NULL DEFAULT 0,
    digest TEXT NOT NULL DEFAULT '', auto_share_existing INTEGER NOT NULL DEFAULT 0,
    auto_share_created INTEGER NOT NULL DEFAULT 0, auto_share_failed INTEGER NOT NULL DEFAULT 0,
    committed_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE (source_id, sequence)
);
CREATE INDEX idx_wedrive_snapshots_source_created ON wedrive_snapshots (source_id, created_at DESC);

CREATE TABLE wedrive_inventory_items (
    id TEXT PRIMARY KEY, tenant_id INTEGER NOT NULL, source_id TEXT NOT NULL,
    snapshot_id TEXT NOT NULL, external_id TEXT NOT NULL, parent_external_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL, path TEXT NOT NULL, item_type TEXT NOT NULL, doc_id TEXT NOT NULL DEFAULT '',
    share_url TEXT NOT NULL DEFAULT '', mime_type TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0,
    modified_at DATETIME, content_fingerprint TEXT NOT NULL DEFAULT '', consumability TEXT NOT NULL DEFAULT 'unknown',
    failure_code TEXT NOT NULL DEFAULT '', missing_count INTEGER NOT NULL DEFAULT 0,
    missing_since DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (snapshot_id, external_id)
);
CREATE INDEX idx_wedrive_items_source_snapshot ON wedrive_inventory_items (source_id, snapshot_id);
