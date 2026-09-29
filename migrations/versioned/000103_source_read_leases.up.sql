-- A question keeps a fixed set of published snapshots until completion.
-- Cleanup removes expired leases before collecting snapshots. Explicit purge
-- invalidates leases first; current source deletion always wins on reads.
CREATE TABLE IF NOT EXISTS source_read_leases (
    id VARCHAR(36) PRIMARY KEY, expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS source_read_leases_expiry ON source_read_leases(expires_at);
CREATE TABLE IF NOT EXISTS source_read_scopes (
    lease_id VARCHAR(36) NOT NULL REFERENCES source_read_leases(id) ON DELETE CASCADE,
    snapshot_id VARCHAR(36) NOT NULL REFERENCES source_snapshots(id),
    data_source_id VARCHAR(36) NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL,
    tenant_id BIGINT NOT NULL, knowledge_ids JSONB NOT NULL, tag_ids JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS source_read_scopes_lease ON source_read_scopes(lease_id, snapshot_id);
CREATE INDEX IF NOT EXISTS source_read_scopes_snapshot ON source_read_scopes(snapshot_id);
CREATE TABLE IF NOT EXISTS source_read_document_scopes (
    lease_id VARCHAR(36) NOT NULL REFERENCES source_read_leases(id) ON DELETE CASCADE,
    knowledge_base_id VARCHAR(36) NOT NULL, tenant_id BIGINT NOT NULL,
    knowledge_ids JSONB NOT NULL, tag_ids JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS source_read_document_scopes_lease ON source_read_document_scopes(lease_id, knowledge_base_id);
