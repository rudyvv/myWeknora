-- Static MyBatis/SQL/Mapper edges are immutable per source snapshot. The
-- parser may report uncertain edges, but consumers must never promote them to
-- a proven call or table relationship.
ALTER TABLE source_file_versions
    ADD COLUMN IF NOT EXISTS facts JSONB NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS diagnostics JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE source_snapshots
    ADD COLUMN IF NOT EXISTS relation_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS relations_staged BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS source_code_relations (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    data_source_id VARCHAR(36) NOT NULL,
    snapshot_id VARCHAR(36) NOT NULL,
    kind VARCHAR(64) NOT NULL,
    from_file_id VARCHAR(36) NOT NULL,
    from_version_id VARCHAR(36) NOT NULL,
    from_path TEXT NOT NULL,
    from_key TEXT NOT NULL DEFAULT '',
    from_range JSONB NOT NULL,
    to_file_id VARCHAR(36) NOT NULL,
    to_version_id VARCHAR(36) NOT NULL,
    to_path TEXT NOT NULL DEFAULT '',
    to_key TEXT NOT NULL DEFAULT '',
    to_range JSONB NOT NULL,
    determinacy VARCHAR(24) NOT NULL,
    quality VARCHAR(24) NOT NULL,
    resolution_reason TEXT NOT NULL DEFAULT '',
    context JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS source_code_relations_snapshot
    ON source_code_relations(tenant_id, data_source_id, snapshot_id);
CREATE INDEX IF NOT EXISTS source_code_relations_from_key
    ON source_code_relations(tenant_id, data_source_id, snapshot_id, from_key);
CREATE INDEX IF NOT EXISTS source_code_relations_to_key
    ON source_code_relations(tenant_id, data_source_id, snapshot_id, to_key);
CREATE INDEX IF NOT EXISTS source_code_relations_from_file
    ON source_code_relations(tenant_id, data_source_id, snapshot_id, from_file_id, from_version_id);
