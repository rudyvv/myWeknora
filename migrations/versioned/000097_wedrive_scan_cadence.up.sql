ALTER TABLE wedrive_sources
    ADD COLUMN scan_interval_minutes INTEGER NOT NULL DEFAULT 30,
    ADD COLUMN next_scan_at TIMESTAMPTZ,
    ADD COLUMN scan_retry_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN scan_state VARCHAR(32) NOT NULL DEFAULT 'idle',
    ADD COLUMN scan_lease_expires_at TIMESTAMPTZ,
    ADD COLUMN last_scan_started_at TIMESTAMPTZ,
    ADD COLUMN last_scan_error_code VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE wedrive_sources
    ADD CONSTRAINT chk_wedrive_scan_interval_minutes
    CHECK (scan_interval_minutes IN (0, 30, 60, 360, 1440));

UPDATE wedrive_sources
SET scan_state = 'waiting',
    next_scan_at = CASE
        WHEN last_complete_scan_at IS NULL THEN NOW()
        WHEN last_complete_scan_at + INTERVAL '30 minutes' < NOW() THEN NOW()
        ELSE last_complete_scan_at + INTERVAL '30 minutes'
    END
WHERE status IN ('awaiting_inventory', 'active') AND deleted_at IS NULL;

CREATE INDEX idx_wedrive_sources_device_next_scan
    ON wedrive_sources (device_id, next_scan_at)
    WHERE deleted_at IS NULL;
