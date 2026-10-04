DROP TABLE IF EXISTS source_cleanup_operations;
ALTER TABLE data_sources
    DROP COLUMN IF EXISTS source_query_enabled,
    DROP COLUMN IF EXISTS source_binding_state;
