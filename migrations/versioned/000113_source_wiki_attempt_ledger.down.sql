DROP TABLE IF EXISTS source_wiki_attempt_calls;

ALTER TABLE source_wiki_attempts
    DROP COLUMN IF EXISTS checkpoint,
    DROP COLUMN IF EXISTS phase,
    DROP COLUMN IF EXISTS max_repairs,
    DROP COLUMN IF EXISTS max_elapsed_ms,
    DROP COLUMN IF EXISTS max_tokens,
    DROP COLUMN IF EXISTS max_calls,
    DROP COLUMN IF EXISTS deadline_at,
    DROP COLUMN IF EXISTS lease_expires_at,
    DROP COLUMN IF EXISTS lease_owner,
    DROP COLUMN IF EXISTS epoch,
    DROP COLUMN IF EXISTS base_page_version,
    DROP COLUMN IF EXISTS max_completion_tokens,
    DROP COLUMN IF EXISTS model_context_window,
    DROP COLUMN IF EXISTS model_settings_fingerprint,
    DROP COLUMN IF EXISTS model_id,
    DROP COLUMN IF EXISTS source_updated_at,
    DROP COLUMN IF EXISTS source_config_fingerprint;
