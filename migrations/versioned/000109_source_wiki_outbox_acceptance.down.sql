DROP INDEX IF EXISTS idx_source_wiki_pending_event;
ALTER TABLE source_publication_outbox
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS config_generation;
ALTER TABLE source_sync_runs
    ADD COLUMN IF NOT EXISTS budget_calls INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS budget_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS budget_repairs INTEGER NOT NULL DEFAULT 0;
