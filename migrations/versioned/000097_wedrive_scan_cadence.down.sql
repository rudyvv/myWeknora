DROP INDEX IF EXISTS idx_wedrive_sources_device_next_scan;
ALTER TABLE wedrive_sources DROP CONSTRAINT IF EXISTS chk_wedrive_scan_interval_minutes;
ALTER TABLE wedrive_sources
    DROP COLUMN IF EXISTS last_scan_error_code,
    DROP COLUMN IF EXISTS last_scan_started_at,
    DROP COLUMN IF EXISTS scan_lease_expires_at,
    DROP COLUMN IF EXISTS scan_state,
    DROP COLUMN IF EXISTS scan_retry_count,
    DROP COLUMN IF EXISTS next_scan_at,
    DROP COLUMN IF EXISTS scan_interval_minutes;
