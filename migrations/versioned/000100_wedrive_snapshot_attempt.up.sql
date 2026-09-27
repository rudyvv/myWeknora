ALTER TABLE wedrive_snapshots
    ADD COLUMN scan_attempt_id VARCHAR(36) NOT NULL DEFAULT '',
    ADD COLUMN root_external_id VARCHAR(255) NOT NULL DEFAULT '';

-- Historical inventories remain unbound. New inventories are unique per
-- source/attempt; sequence is retained as inventory metadata.
CREATE UNIQUE INDEX idx_wedrive_snapshots_source_attempt
    ON wedrive_snapshots (source_id, scan_attempt_id)
    WHERE scan_attempt_id <> '';
