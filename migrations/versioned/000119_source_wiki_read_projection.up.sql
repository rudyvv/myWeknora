-- An in-flight question keeps the exact validated source-backed Wiki cards
-- observed while its publication locks were held. The projection is immutable
-- and owned by the question lease; raw evidence owners use the existing GC
-- lifecycle in source_read_wiki_evidence_refs.
CREATE TABLE source_read_wiki_projection_captures (
    lease_id VARCHAR(36) PRIMARY KEY REFERENCES source_read_leases(id) ON DELETE CASCADE,
    status VARCHAR(32) NOT NULL CHECK (status IN ('captured', 'capacity_exceeded')),
    page_count BIGINT NOT NULL CHECK (page_count >= 0),
    projection_bytes BIGINT NOT NULL CHECK (projection_bytes >= 0),
    evidence_owner_count BIGINT NOT NULL CHECK (evidence_owner_count >= 0)
);

CREATE TABLE source_read_wiki_page_projections (
    lease_id VARCHAR(36) NOT NULL REFERENCES source_read_wiki_projection_captures(lease_id) ON DELETE CASCADE,
    page_id VARCHAR(36) NOT NULL,
    page_version INTEGER NOT NULL CHECK (page_version > 0),
    page_snapshot JSONB NOT NULL CHECK (jsonb_typeof(page_snapshot) = 'object'),
    contributions JSONB NOT NULL CHECK (jsonb_typeof(contributions) = 'array'),
    PRIMARY KEY (lease_id, page_id, page_version)
);

CREATE INDEX source_read_wiki_page_projections_lease_slug
    ON source_read_wiki_page_projections(lease_id, (page_snapshot->>'knowledge_base_id'), (page_snapshot->>'slug'));
