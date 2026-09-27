ALTER TABLE wedrive_sources ADD COLUMN scan_interval_minutes INTEGER NOT NULL DEFAULT 30;
ALTER TABLE wedrive_sources ADD COLUMN next_scan_at DATETIME;
ALTER TABLE wedrive_sources ADD COLUMN scan_retry_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE wedrive_sources ADD COLUMN scan_state TEXT NOT NULL DEFAULT 'idle';
ALTER TABLE wedrive_sources ADD COLUMN scan_lease_expires_at DATETIME;
ALTER TABLE wedrive_sources ADD COLUMN last_scan_started_at DATETIME;
ALTER TABLE wedrive_sources ADD COLUMN last_scan_error_code TEXT NOT NULL DEFAULT '';

UPDATE wedrive_sources
SET scan_state = 'waiting',
    next_scan_at = CASE
        WHEN last_complete_scan_at IS NULL THEN CURRENT_TIMESTAMP
        WHEN datetime(last_complete_scan_at, '+30 minutes') < CURRENT_TIMESTAMP THEN CURRENT_TIMESTAMP
        ELSE datetime(last_complete_scan_at, '+30 minutes')
    END
WHERE status IN ('awaiting_inventory', 'active') AND deleted_at IS NULL;

CREATE INDEX idx_wedrive_sources_device_next_scan
    ON wedrive_sources (device_id, next_scan_at);
