-- Immutable source membership and a publication pointer on the built-in PG DB.
CREATE TABLE IF NOT EXISTS source_snapshots (
    id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL, data_source_id VARCHAR(36) NOT NULL,
    sync_log_id VARCHAR(36) NOT NULL UNIQUE, project_id TEXT NOT NULL,
    commit_sha TEXT NOT NULL, repository_url TEXT NOT NULL, rules_version TEXT NOT NULL,
    state TEXT NOT NULL, manifest_complete BOOLEAN NOT NULL DEFAULT FALSE,
    manifest_digest TEXT NOT NULL, member_count INTEGER NOT NULL,
    file_count INTEGER NOT NULL DEFAULT 0, chunk_count INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now(), published_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS source_snapshots_source ON source_snapshots(data_source_id);
CREATE TABLE IF NOT EXISTS source_files (
    id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL, data_source_id VARCHAR(36) NOT NULL,
    path TEXT NOT NULL, UNIQUE(data_source_id, path)
);
CREATE TABLE IF NOT EXISTS source_file_versions (
    id VARCHAR(36) PRIMARY KEY, source_file_id VARCHAR(36) NOT NULL REFERENCES source_files(id),
    snapshot_id VARCHAR(36) NOT NULL REFERENCES source_snapshots(id), blob_sha TEXT NOT NULL,
    sha256 TEXT NOT NULL, content BYTEA NOT NULL, encoding TEXT NOT NULL,
    parser_version TEXT NOT NULL, quality TEXT NOT NULL, symbols JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS source_versions_file ON source_file_versions(source_file_id);
CREATE INDEX IF NOT EXISTS source_versions_snapshot ON source_file_versions(snapshot_id);
CREATE TABLE IF NOT EXISTS source_snapshot_members (
    snapshot_id VARCHAR(36) NOT NULL REFERENCES source_snapshots(id), path TEXT NOT NULL,
    source_file_id VARCHAR(36) NOT NULL DEFAULT '', file_version_id VARCHAR(36) NOT NULL DEFAULT '',
    blob_sha TEXT NOT NULL, size BIGINT NOT NULL, status TEXT NOT NULL, reason TEXT NOT NULL,
    encoding TEXT NOT NULL DEFAULT '', generated BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY(snapshot_id, path)
);
CREATE INDEX IF NOT EXISTS source_members_file ON source_snapshot_members(source_file_id);
CREATE INDEX IF NOT EXISTS source_members_version ON source_snapshot_members(file_version_id);
CREATE TABLE IF NOT EXISTS source_chunk_references (
    chunk_id VARCHAR(36) PRIMARY KEY, snapshot_id VARCHAR(36) NOT NULL REFERENCES source_snapshots(id),
    source_file_id VARCHAR(36) NOT NULL REFERENCES source_files(id),
    file_version_id VARCHAR(36) NOT NULL REFERENCES source_file_versions(id)
);
CREATE INDEX IF NOT EXISTS source_chunks_snapshot ON source_chunk_references(snapshot_id);
CREATE INDEX IF NOT EXISTS source_chunks_file ON source_chunk_references(source_file_id);
CREATE INDEX IF NOT EXISTS source_chunks_version ON source_chunk_references(file_version_id);
CREATE TABLE IF NOT EXISTS source_publications (
    data_source_id VARCHAR(36) PRIMARY KEY, snapshot_id VARCHAR(36) NOT NULL REFERENCES source_snapshots(id),
    tenant_id BIGINT NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL
);
CREATE INDEX IF NOT EXISTS source_publications_snapshot ON source_publications(snapshot_id);
