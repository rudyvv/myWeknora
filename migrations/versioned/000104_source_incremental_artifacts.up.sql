-- Cache computation, never publication membership or evidence coordinates.
CREATE TABLE source_parsed_artifacts (
 tenant_id BIGINT NOT NULL, data_source_id VARCHAR(36) NOT NULL,
 artifact_key TEXT NOT NULL, parsed JSONB NOT NULL,
 PRIMARY KEY (tenant_id,data_source_id,artifact_key)
);
CREATE TABLE source_embedding_artifacts (
 tenant_id BIGINT NOT NULL, data_source_id VARCHAR(36) NOT NULL,
 artifact_key TEXT NOT NULL, vector JSONB NOT NULL,
 PRIMARY KEY (tenant_id,data_source_id,artifact_key)
);
-- A path can move and can later name a different logical file.
ALTER TABLE source_files DROP CONSTRAINT IF EXISTS source_files_data_source_id_path_key;
ALTER TABLE source_snapshots
 ADD COLUMN processing_version TEXT NOT NULL DEFAULT '',
 ADD COLUMN embedding_version TEXT NOT NULL DEFAULT '',
 ADD COLUMN previous_snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
 ADD COLUMN previous_commit_sha TEXT NOT NULL DEFAULT '',
 ADD COLUMN added_count INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN changed_count INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN deleted_count INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN renamed_count INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN parsed_count INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN reused_file_count INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN reused_chunk_count INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN embedded_chunk_count INTEGER NOT NULL DEFAULT 0,
 ADD COLUMN reused_vector_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_snapshot_members
 ADD COLUMN change TEXT NOT NULL DEFAULT '',
 ADD COLUMN previous_path TEXT NOT NULL DEFAULT '',
 ADD COLUMN parse_reused BOOLEAN NOT NULL DEFAULT FALSE;
