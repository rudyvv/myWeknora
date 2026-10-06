ALTER TABLE source_snapshots
    ADD COLUMN wiki_derivation_state VARCHAR(32) NOT NULL DEFAULT 'pending';

UPDATE source_snapshots
SET wiki_derivation_state = CASE WHEN relations_staged THEN 'complete' ELSE 'pending' END;

ALTER TABLE source_snapshots
    ADD CONSTRAINT source_snapshots_wiki_derivation_state_check
    CHECK (wiki_derivation_state IN ('pending', 'complete', 'deferred_capacity'));
