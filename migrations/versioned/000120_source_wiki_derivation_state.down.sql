ALTER TABLE source_snapshots
    DROP CONSTRAINT IF EXISTS source_snapshots_wiki_derivation_state_check,
    DROP COLUMN IF EXISTS wiki_derivation_state;
