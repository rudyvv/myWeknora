DROP TRIGGER IF EXISTS source_wiki_evidence_gc_candidate ON source_wiki_evidence_refs;
DROP TRIGGER IF EXISTS source_wiki_attempt_evidence_gc_candidate ON source_wiki_attempt_evidence_refs;
DROP TRIGGER IF EXISTS source_read_wiki_evidence_gc_candidate ON source_read_wiki_evidence_refs;
DROP FUNCTION IF EXISTS enqueue_source_snapshot_gc_after_wiki_evidence_release();
DROP TABLE IF EXISTS source_snapshot_gc_candidates;
DROP TABLE IF EXISTS source_wiki_attempt_evidence_refs;
DROP TABLE IF EXISTS source_read_wiki_evidence_refs;
ALTER TABLE source_wiki_evidence_refs DROP COLUMN IF EXISTS path, DROP COLUMN IF EXISTS commit_sha;
