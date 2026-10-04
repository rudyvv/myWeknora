CREATE TABLE source_wiki_update_plans (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    source_id VARCHAR(36) NOT NULL,
    previous_snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
    snapshot_id VARCHAR(36) NOT NULL,
    config_generation BIGINT NOT NULL,
    plan_digest VARCHAR(64) NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    source_wide_stale BOOLEAN NOT NULL DEFAULT FALSE,
    reason_code TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    plan JSONB NOT NULL,
    next_inventory JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    UNIQUE (source_id, snapshot_id),
    CHECK (status IN ('pending', 'running', 'completed', 'failed', 'superseded')),
    CHECK (jsonb_typeof(plan) = 'object'),
    CHECK (jsonb_typeof(next_inventory) = 'object')
);
CREATE INDEX source_wiki_update_plans_lane
    ON source_wiki_update_plans(knowledge_base_id, source_id, status, created_at);
CREATE INDEX source_wiki_update_plans_target
    ON source_wiki_update_plans(source_id, snapshot_id, config_generation);

CREATE TABLE source_wiki_update_plan_items (
    id BIGSERIAL PRIMARY KEY,
    plan_id VARCHAR(36) NOT NULL REFERENCES source_wiki_update_plans(id) ON DELETE CASCADE,
    topic_key TEXT NOT NULL,
    page_id VARCHAR(36),
    action TEXT NOT NULL,
    reason_code TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'pending',
    expected_page_version INTEGER NOT NULL DEFAULT 0,
    evidence_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    dependency_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    module_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (plan_id, topic_key),
    CHECK (action IN ('regenerate', 'carry_forward', 'remove', 'skeleton')),
    CHECK (state IN ('pending', 'running', 'completed', 'failed', 'superseded')),
    CHECK (jsonb_typeof(details) = 'object')
);
CREATE INDEX source_wiki_update_plan_items_work
    ON source_wiki_update_plan_items(plan_id, state, action, topic_key);
CREATE INDEX source_wiki_update_plan_items_page
    ON source_wiki_update_plan_items(page_id, state);

-- One current source contribution per (page, source), plus immutable snapshots
-- for each retained Wiki revision. A blank dependency inventory is never
-- treated as proof that a legacy page is unaffected.
CREATE TABLE source_wiki_page_contributions (
    id BIGSERIAL PRIMARY KEY,
    page_id VARCHAR(36) NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
    revision_id VARCHAR(36) REFERENCES wiki_page_revisions(id) ON DELETE CASCADE,
    source_id VARCHAR(36) NOT NULL,
    page_version INTEGER NOT NULL,
    topic_kind TEXT NOT NULL DEFAULT '',
    topic_key TEXT NOT NULL DEFAULT '',
    applicable_snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
    target_snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    reason_code TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    evidence_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    dependency_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    module_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    dependency_file_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    module_member_file_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    contribution JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (page_version > 0),
    CHECK (state IN ('ready', 'stale', 'pending', 'unverified', 'removed')),
    CHECK (jsonb_typeof(dependency_file_ids) = 'array'),
    CHECK (jsonb_typeof(module_member_file_ids) = 'array'),
    CHECK (jsonb_typeof(contribution) = 'object')
);
CREATE UNIQUE INDEX source_wiki_page_contributions_current
    ON source_wiki_page_contributions(page_id, source_id, topic_kind, topic_key)
    WHERE revision_id IS NULL;
CREATE UNIQUE INDEX source_wiki_page_contributions_revision
    ON source_wiki_page_contributions(revision_id, source_id, topic_kind, topic_key)
    WHERE revision_id IS NOT NULL;
CREATE INDEX source_wiki_page_contributions_source_state
    ON source_wiki_page_contributions(source_id, state, target_snapshot_id);
CREATE INDEX source_wiki_page_contributions_page
    ON source_wiki_page_contributions(page_id, source_id, revision_id);

-- Legacy provenance does not contain complete dependency or module manifests.
-- Preserve the old body/evidence and exact owner references, but backfill it as
-- stale so it must be revalidated before it can answer current-source Q&A.
INSERT INTO source_wiki_page_contributions (
    page_id, revision_id, source_id, page_version, topic_kind, topic_key,
    applicable_snapshot_id, target_snapshot_id, state, reason_code, reason,
    dependency_file_ids, module_member_file_ids, contribution, updated_at
)
SELECT p.id, NULL, p.source_provenance->>'source_id', p.version,
       COALESCE(p.source_provenance->>'topic_kind', ''),
       COALESCE(p.source_provenance->>'topic_key', ''),
       COALESCE(p.source_provenance->>'applicable_snapshot_id', ''),
       COALESCE(p.source_provenance->>'applicable_snapshot_id', ''),
       CASE WHEN p.source_provenance->>'state' = 'ready' THEN 'stale'
            WHEN p.source_provenance->>'state' IN ('stale', 'unverified') THEN p.source_provenance->>'state'
            ELSE 'stale' END,
       'legacy_dependency_manifest_missing',
       'Legacy source Wiki contribution lacks complete dependency and module inventories; revalidation is required.',
       '[]'::jsonb, '[]'::jsonb,
       jsonb_build_object(
           'title', p.title, 'summary', p.summary, 'content', p.content,
           'source_refs', COALESCE(p.source_refs::jsonb, '[]'::jsonb),
           'source_provenance', p.source_provenance,
           'has_unattributed_body', TRUE
       ), now()
FROM wiki_pages p
WHERE jsonb_typeof(p.source_provenance::jsonb) = 'object'
  AND COALESCE(p.source_provenance->>'source_id', '') <> ''
ON CONFLICT DO NOTHING;

INSERT INTO source_wiki_page_contributions (
    page_id, revision_id, source_id, page_version, topic_kind, topic_key,
    applicable_snapshot_id, target_snapshot_id, state, reason_code, reason,
    dependency_file_ids, module_member_file_ids, contribution, updated_at
)
SELECT r.page_id, r.id, r.source_provenance->>'source_id', r.version,
       COALESCE(r.source_provenance->>'topic_kind', ''),
       COALESCE(r.source_provenance->>'topic_key', ''),
       COALESCE(r.source_provenance->>'applicable_snapshot_id', ''),
       COALESCE(r.source_provenance->>'applicable_snapshot_id', ''),
       CASE WHEN r.source_provenance->>'state' = 'ready' THEN 'stale'
            WHEN r.source_provenance->>'state' IN ('stale', 'unverified') THEN r.source_provenance->>'state'
            ELSE 'stale' END,
       'legacy_dependency_manifest_missing',
       'Legacy source Wiki revision lacks complete dependency and module inventories; retained only for authorized history.',
       '[]'::jsonb, '[]'::jsonb,
       jsonb_build_object(
           'title', r.title, 'summary', r.summary, 'content', r.content,
           'source_refs', COALESCE(r.source_refs::jsonb, '[]'::jsonb),
           'source_provenance', r.source_provenance,
           'has_unattributed_body', TRUE
       ), now()
FROM wiki_page_revisions r
WHERE jsonb_typeof(r.source_provenance::jsonb) = 'object'
  AND COALESCE(r.source_provenance->>'source_id', '') <> ''
ON CONFLICT DO NOTHING;
