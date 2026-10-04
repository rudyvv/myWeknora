-- The legacy keys cannot represent mixed-source local-ID collisions. Refuse
-- rollback before changing schema if exact owners have diverged under those
-- keys; never discard an owner to make the old constraint fit.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM source_wiki_evidence_refs
        WHERE revision_id IS NULL
        GROUP BY page_id,version,evidence_id HAVING count(*)>1
    ) OR EXISTS (
        SELECT 1 FROM source_wiki_evidence_refs
        WHERE revision_id IS NOT NULL
        GROUP BY revision_id,evidence_id HAVING count(*)>1
    ) OR EXISTS (
        SELECT 1 FROM source_read_wiki_evidence_refs
        GROUP BY lease_id,page_id,version,evidence_id HAVING count(*)>1
    ) THEN
        RAISE EXCEPTION 'cannot roll back mixed Wiki evidence owners: legacy owner keys would collide';
    END IF;
END;
$$;

DROP INDEX IF EXISTS source_read_wiki_current_evidence;
DROP INDEX IF EXISTS source_read_wiki_revision_evidence;
ALTER TABLE source_read_wiki_evidence_refs
    ADD CONSTRAINT source_read_wiki_evidence_refs_pkey PRIMARY KEY (lease_id,page_id,version,evidence_id);

DROP INDEX IF EXISTS source_wiki_current_evidence;
DROP INDEX IF EXISTS source_wiki_revision_evidence;
CREATE UNIQUE INDEX source_wiki_current_evidence
    ON source_wiki_evidence_refs(page_id,version,evidence_id)
    WHERE revision_id IS NULL;
CREATE UNIQUE INDEX source_wiki_revision_evidence
    ON source_wiki_evidence_refs(revision_id,evidence_id)
    WHERE revision_id IS NOT NULL;
