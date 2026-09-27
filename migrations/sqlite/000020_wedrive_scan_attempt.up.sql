ALTER TABLE wedrive_sources ADD COLUMN scan_attempt_id TEXT NOT NULL DEFAULT '';

-- Keep completed inventories intact while releasing identity-less scans.
UPDATE wedrive_sources
SET scan_state = CASE WHEN scan_interval_minutes > 0 THEN 'retry_wait' ELSE 'failed' END,
    next_scan_at = CASE WHEN scan_interval_minutes > 0 THEN datetime('now', '+10 minutes') ELSE NULL END,
    scan_retry_count = CASE WHEN scan_interval_minutes > 0 THEN 1 ELSE 0 END,
    scan_lease_expires_at = NULL,
    last_scan_error_code = 'scan_protocol_upgrade'
WHERE scan_state = 'running' AND scan_attempt_id = '';
