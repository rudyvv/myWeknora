-- Add a stable source configuration epoch to each outbox event and enforce
-- receiver idempotency by publication event id in the dedicated Wiki lane.
ALTER TABLE source_publication_outbox
    ADD COLUMN IF NOT EXISTS config_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Source-run Wiki/model budgets belong to source_wiki_attempts (T14), not to
-- the trigger coordinator. T06 only tracks its bounded retry_count.
ALTER TABLE source_sync_runs
    DROP COLUMN IF EXISTS budget_calls,
    DROP COLUMN IF EXISTS budget_tokens,
    DROP COLUMN IF EXISTS budget_repairs;

CREATE UNIQUE INDEX IF NOT EXISTS idx_source_wiki_pending_event
    ON task_pending_ops (task_type, scope, scope_id, op, dedup_key)
    WHERE task_type = 'source:wiki:update';
