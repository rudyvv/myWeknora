DROP INDEX idx_wedrive_snapshots_source_attempt;
ALTER TABLE wedrive_snapshots DROP COLUMN root_external_id;
ALTER TABLE wedrive_snapshots DROP COLUMN scan_attempt_id;
