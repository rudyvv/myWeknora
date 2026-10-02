ALTER TABLE source_wiki_attempts
    ADD COLUMN IF NOT EXISTS source_config_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_updated_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    ADD COLUMN IF NOT EXISTS model_id VARCHAR(36) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS model_settings_fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS model_context_window INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS max_completion_tokens INTEGER NOT NULL DEFAULT 4096,
    ADD COLUMN IF NOT EXISTS base_page_version INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS epoch BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS lease_owner VARCHAR(36) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS deadline_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS max_calls INTEGER NOT NULL DEFAULT 18,
    ADD COLUMN IF NOT EXISTS max_tokens INTEGER NOT NULL DEFAULT 360000,
    ADD COLUMN IF NOT EXISTS max_elapsed_ms BIGINT NOT NULL DEFAULT 180000,
    ADD COLUMN IF NOT EXISTS max_repairs INTEGER NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS phase TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS checkpoint JSONB;

-- Existing attempts receive one finite deadline measured from their original
-- creation time; migration never refreshes their consumed call/token counters.
UPDATE source_wiki_attempts
SET deadline_at = created_at + (max_elapsed_ms * INTERVAL '1 millisecond')
WHERE deadline_at IS NULL;
ALTER TABLE source_wiki_attempts ALTER COLUMN deadline_at SET NOT NULL;

CREATE TABLE IF NOT EXISTS source_wiki_attempt_calls (
    id VARCHAR(36) PRIMARY KEY,
    attempt_id VARCHAR(36) NOT NULL REFERENCES source_wiki_attempts(id) ON DELETE CASCADE,
    epoch BIGINT NOT NULL,
    call_number INTEGER NOT NULL,
    phase TEXT NOT NULL,
    reserved_tokens INTEGER NOT NULL,
    actual_tokens INTEGER,
    outcome TEXT NOT NULL DEFAULT 'reserved',
    created_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    UNIQUE (attempt_id, call_number)
);
CREATE INDEX IF NOT EXISTS source_wiki_attempt_calls_attempt
    ON source_wiki_attempt_calls(attempt_id, call_number);
