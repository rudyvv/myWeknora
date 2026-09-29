ALTER TABLE wiki_pages ADD COLUMN IF NOT EXISTS source_provenance JSONB;
ALTER TABLE wiki_page_revisions ADD COLUMN IF NOT EXISTS source_refs JSONB;
ALTER TABLE wiki_page_revisions ADD COLUMN IF NOT EXISTS chunk_refs JSONB;
ALTER TABLE wiki_page_revisions ADD COLUMN IF NOT EXISTS page_metadata JSONB;
ALTER TABLE wiki_page_revisions ADD COLUMN IF NOT EXISTS source_provenance JSONB;

CREATE TABLE IF NOT EXISTS source_wiki_attempts (
 id VARCHAR(36) PRIMARY KEY,
 tenant_id BIGINT NOT NULL,
 knowledge_base_id VARCHAR(36) NOT NULL,
 source_id VARCHAR(36) NOT NULL,
 snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
 module_path TEXT NOT NULL,
 title TEXT NOT NULL,
 slug TEXT NOT NULL,
 status TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 draft JSONB,
 calls INTEGER NOT NULL DEFAULT 0,
 tokens INTEGER NOT NULL DEFAULT 0,
 repairs INTEGER NOT NULL DEFAULT 0,
 created_at TIMESTAMPTZ NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS source_wiki_one_running_module
 ON source_wiki_attempts(tenant_id,knowledge_base_id,source_id,module_path) WHERE status='running';
CREATE INDEX IF NOT EXISTS source_wiki_attempts_kb ON source_wiki_attempts(knowledge_base_id,created_at);

-- Every retained body owns fixed raw evidence, beginning with version one.
-- RESTRICT prevents future collection from discarding referenced raw versions.
CREATE TABLE IF NOT EXISTS source_wiki_evidence_refs (
 id VARCHAR(36) PRIMARY KEY,
 page_id VARCHAR(36) NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
 revision_id VARCHAR(36) REFERENCES wiki_page_revisions(id) ON DELETE CASCADE,
 version INTEGER NOT NULL,
 evidence_id TEXT NOT NULL,
 source_file_id VARCHAR(36) NOT NULL REFERENCES source_files(id) ON DELETE RESTRICT,
 file_version_id VARCHAR(36) NOT NULL REFERENCES source_file_versions(id) ON DELETE RESTRICT,
 snapshot_id VARCHAR(36) NOT NULL REFERENCES source_snapshots(id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX IF NOT EXISTS source_wiki_current_evidence
 ON source_wiki_evidence_refs(page_id,version,evidence_id) WHERE revision_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS source_wiki_revision_evidence
 ON source_wiki_evidence_refs(revision_id,evidence_id) WHERE revision_id IS NOT NULL;


-- Permission alternatives are independent of current member contribution.
-- Only Wiki registered-body readers use these; ordinary source readers keep
-- the exact pinned publication scopes from 000103.
CREATE TABLE IF NOT EXISTS source_read_wiki_scopes (
 id BIGSERIAL PRIMARY KEY,
 lease_id VARCHAR(36) NOT NULL REFERENCES source_read_leases(id) ON DELETE CASCADE,
 knowledge_base_id VARCHAR(36) NOT NULL,
 tenant_id BIGINT NOT NULL,
 source_ids JSONB NOT NULL,
 knowledge_ids JSONB NOT NULL,
 tag_ids JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS source_read_wiki_scopes_lease ON source_read_wiki_scopes(lease_id);
ALTER TABLE source_wiki_attempts ADD COLUMN IF NOT EXISTS evidence_knowledge_ids JSONB;
