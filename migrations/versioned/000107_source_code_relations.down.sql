DROP TABLE IF EXISTS source_code_relations;
ALTER TABLE source_snapshots DROP COLUMN IF EXISTS relations_staged, DROP COLUMN IF EXISTS relation_count;
ALTER TABLE source_file_versions DROP COLUMN IF EXISTS diagnostics, DROP COLUMN IF EXISTS facts;
