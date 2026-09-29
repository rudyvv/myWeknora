DROP TABLE IF EXISTS source_embedding_artifacts;
DROP TABLE IF EXISTS source_parsed_artifacts;
ALTER TABLE source_snapshot_members DROP COLUMN IF EXISTS change, DROP COLUMN IF EXISTS previous_path, DROP COLUMN IF EXISTS parse_reused;
ALTER TABLE source_snapshots DROP COLUMN IF EXISTS processing_version, DROP COLUMN IF EXISTS embedding_version, DROP COLUMN IF EXISTS previous_snapshot_id, DROP COLUMN IF EXISTS previous_commit_sha, DROP COLUMN IF EXISTS added_count, DROP COLUMN IF EXISTS changed_count, DROP COLUMN IF EXISTS deleted_count, DROP COLUMN IF EXISTS renamed_count, DROP COLUMN IF EXISTS parsed_count, DROP COLUMN IF EXISTS reused_file_count, DROP COLUMN IF EXISTS reused_chunk_count, DROP COLUMN IF EXISTS embedded_chunk_count, DROP COLUMN IF EXISTS reused_vector_count;
-- Do not restore path uniqueness: retained rename history may legitimately collide.
