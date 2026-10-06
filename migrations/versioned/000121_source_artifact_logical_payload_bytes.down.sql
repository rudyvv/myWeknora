ALTER TABLE source_embedding_artifacts
    DROP CONSTRAINT IF EXISTS source_embedding_artifacts_logical_payload_bytes_check,
    DROP COLUMN IF EXISTS logical_payload_bytes;

ALTER TABLE source_parsed_artifacts
    DROP CONSTRAINT IF EXISTS source_parsed_artifacts_logical_payload_bytes_check,
    DROP COLUMN IF EXISTS logical_payload_bytes;
