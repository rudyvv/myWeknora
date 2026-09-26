ALTER TABLE wedrive_snapshots ADD COLUMN scan_attempt_id TEXT NOT NULL DEFAULT '';
ALTER TABLE wedrive_snapshots ADD COLUMN root_external_id TEXT NOT NULL DEFAULT '';

-- Empty attempt IDs on historical inventories are intentionally excluded.
CREATE UNIQUE INDEX idx_wedrive_snapshots_source_attempt
    ON wedrive_snapshots (source_id, scan_attempt_id)
    WHERE scan_attempt_id <> '';
