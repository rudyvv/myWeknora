DROP TABLE IF EXISTS source_read_wiki_scopes;
DROP TABLE IF EXISTS source_wiki_evidence_refs;
DROP TABLE IF EXISTS source_wiki_attempts;
ALTER TABLE wiki_page_revisions DROP COLUMN IF EXISTS source_provenance;
ALTER TABLE wiki_page_revisions DROP COLUMN IF EXISTS page_metadata;
ALTER TABLE wiki_page_revisions DROP COLUMN IF EXISTS chunk_refs;
ALTER TABLE wiki_page_revisions DROP COLUMN IF EXISTS source_refs;
ALTER TABLE wiki_pages DROP COLUMN IF EXISTS source_provenance;
