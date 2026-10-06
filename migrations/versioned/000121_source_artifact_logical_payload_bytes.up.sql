ALTER TABLE source_parsed_artifacts
    ADD COLUMN logical_payload_bytes BIGINT,
    ADD CONSTRAINT source_parsed_artifacts_logical_payload_bytes_check
        CHECK (logical_payload_bytes IS NULL OR logical_payload_bytes = octet_length(parsed::text)) NOT VALID;

ALTER TABLE source_embedding_artifacts
    ADD COLUMN logical_payload_bytes BIGINT,
    ADD CONSTRAINT source_embedding_artifacts_logical_payload_bytes_check
        CHECK (
            logical_payload_bytes IS NULL OR
            CASE WHEN jsonb_typeof(vector) = 'array'
                THEN logical_payload_bytes = jsonb_array_length(vector)::BIGINT * 4
                ELSE FALSE
            END
        ) NOT VALID;
