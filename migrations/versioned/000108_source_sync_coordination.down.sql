DROP TABLE IF EXISTS source_publication_outbox;
DROP TABLE IF EXISTS source_sync_runs;
DROP TABLE IF EXISTS source_sync_states;
ALTER TABLE sync_logs
    DROP COLUMN IF EXISTS source_fencing_token,
    DROP COLUMN IF EXISTS source_config_generation;
