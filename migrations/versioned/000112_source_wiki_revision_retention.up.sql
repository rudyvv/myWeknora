-- History readers pin exact immutable versions for the lifetime of their
-- durable source-read lease. Deleting the lease releases these short-lived
-- owners automatically.
CREATE TABLE IF NOT EXISTS source_read_wiki_evidence_refs (
    lease_id VARCHAR(36) NOT NULL REFERENCES source_read_leases(id) ON DELETE CASCADE,
    page_id VARCHAR(36) NOT NULL,
    revision_id VARCHAR(36),
    version INTEGER NOT NULL,
    evidence_id TEXT NOT NULL,
    source_file_id VARCHAR(36) NOT NULL REFERENCES source_files(id) ON DELETE RESTRICT,
    file_version_id VARCHAR(36) NOT NULL REFERENCES source_file_versions(id) ON DELETE RESTRICT,
    snapshot_id VARCHAR(36) NOT NULL REFERENCES source_snapshots(id) ON DELETE RESTRICT,
    path TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    PRIMARY KEY (lease_id, page_id, version, evidence_id)
);
CREATE INDEX IF NOT EXISTS source_read_wiki_evidence_version
    ON source_read_wiki_evidence_refs(file_version_id);

-- Retained body evidence carries its immutable display coordinates. Snapshot
-- membership may then be collected with the old index without making history
-- depend on that retrieval projection.
ALTER TABLE source_wiki_evidence_refs
    ADD COLUMN IF NOT EXISTS path TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS commit_sha TEXT NOT NULL DEFAULT '';
UPDATE source_wiki_evidence_refs wr
SET path=evidence.item->>'path', commit_sha=evidence.item->>'commit_sha'
FROM wiki_pages wp
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(wp.source_provenance->'evidence')='array' THEN wp.source_provenance->'evidence' ELSE '[]'::jsonb END) AS evidence(item)
WHERE wr.revision_id IS NULL AND wp.id=wr.page_id
  AND evidence.item->>'id'=wr.evidence_id
  AND evidence.item->>'knowledge_id'=wr.source_file_id
  AND evidence.item->>'file_version_id'=wr.file_version_id;
UPDATE source_wiki_evidence_refs wr
SET path=evidence.item->>'path', commit_sha=evidence.item->>'commit_sha'
FROM wiki_page_revisions rev
CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(rev.source_provenance->'evidence')='array' THEN rev.source_provenance->'evidence' ELSE '[]'::jsonb END) AS evidence(item)
WHERE wr.revision_id IS NOT NULL AND rev.id=wr.revision_id
  AND evidence.item->>'id'=wr.evidence_id
  AND evidence.item->>'knowledge_id'=wr.source_file_id
  AND evidence.item->>'file_version_id'=wr.file_version_id;

-- Source Wiki attempts pin the exact versions used by an in-flight model run.
-- This owner is independent of the ordinary source-read lease's expiry.
CREATE TABLE IF NOT EXISTS source_wiki_attempt_evidence_refs (
    attempt_id VARCHAR(36) NOT NULL REFERENCES source_wiki_attempts(id) ON DELETE CASCADE,
    source_file_id VARCHAR(36) NOT NULL REFERENCES source_files(id) ON DELETE RESTRICT,
    file_version_id VARCHAR(36) NOT NULL REFERENCES source_file_versions(id) ON DELETE RESTRICT,
    snapshot_id VARCHAR(36) NOT NULL REFERENCES source_snapshots(id) ON DELETE RESTRICT,
    PRIMARY KEY (attempt_id,file_version_id)
);
CREATE INDEX IF NOT EXISTS source_wiki_attempt_evidence_version
    ON source_wiki_attempt_evidence_refs(file_version_id);
INSERT INTO source_wiki_attempt_evidence_refs(attempt_id,source_file_id,file_version_id,snapshot_id)
SELECT a.id,sm.source_file_id,sm.file_version_id,sm.snapshot_id
FROM source_wiki_attempts a
JOIN source_snapshot_members sm ON sm.snapshot_id=a.snapshot_id AND sm.status='parsed'
WHERE a.status='running' AND jsonb_exists(a.evidence_knowledge_ids,sm.source_file_id)
ON CONFLICT (attempt_id,file_version_id) DO NOTHING;

-- Retired snapshots lose their index independently from any immutable raw
-- versions still owned by a current page, retained revision or read lease.
CREATE TABLE IF NOT EXISTS source_snapshot_gc_candidates (
    snapshot_id VARCHAR(36) PRIMARY KEY REFERENCES source_snapshots(id) ON DELETE CASCADE,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    claim_token VARCHAR(36),
    claimed_until TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS source_snapshot_gc_due
    ON source_snapshot_gc_candidates(next_attempt_at, claimed_until, enqueued_at);

-- Upgrade installs may already have accumulated superseded publications.
INSERT INTO source_snapshot_gc_candidates(snapshot_id)
SELECT ss.id
FROM source_snapshots ss
WHERE ss.state='published'
  AND NOT EXISTS (SELECT 1 FROM source_publications sp WHERE sp.snapshot_id=ss.id)
ON CONFLICT (snapshot_id) DO NOTHING;

CREATE OR REPLACE FUNCTION enqueue_source_snapshot_gc_after_wiki_evidence_release()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO source_snapshot_gc_candidates(snapshot_id)
    VALUES (OLD.snapshot_id)
    ON CONFLICT (snapshot_id) DO UPDATE
      SET next_attempt_at = LEAST(source_snapshot_gc_candidates.next_attempt_at, now()),
          last_error = '';
    RETURN OLD;
END;
$$;

DROP TRIGGER IF EXISTS source_wiki_evidence_gc_candidate ON source_wiki_evidence_refs;
CREATE TRIGGER source_wiki_evidence_gc_candidate
AFTER DELETE ON source_wiki_evidence_refs
FOR EACH ROW EXECUTE FUNCTION enqueue_source_snapshot_gc_after_wiki_evidence_release();
DROP TRIGGER IF EXISTS source_wiki_attempt_evidence_gc_candidate ON source_wiki_attempt_evidence_refs;
CREATE TRIGGER source_wiki_attempt_evidence_gc_candidate
AFTER DELETE ON source_wiki_attempt_evidence_refs
FOR EACH ROW EXECUTE FUNCTION enqueue_source_snapshot_gc_after_wiki_evidence_release();
DROP TRIGGER IF EXISTS source_read_wiki_evidence_gc_candidate ON source_read_wiki_evidence_refs;
CREATE TRIGGER source_read_wiki_evidence_gc_candidate
AFTER DELETE ON source_read_wiki_evidence_refs
FOR EACH ROW EXECUTE FUNCTION enqueue_source_snapshot_gc_after_wiki_evidence_release();
