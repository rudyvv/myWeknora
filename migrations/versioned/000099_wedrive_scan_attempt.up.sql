ALTER TABLE wedrive_sources
    ADD COLUMN scan_attempt_id VARCHAR(36) NOT NULL DEFAULT '';

-- Legacy running rows have no attempt identity. Recover them without touching
-- successful inventories. Automatic sources receive one ten-minute retry;
-- manual sources can be claimed again explicitly.
UPDATE wedrive_sources
SET scan_state = CASE WHEN scan_interval_minutes > 0 THEN 'retry_wait' ELSE 'failed' END,
    next_scan_at = CASE WHEN scan_interval_minutes > 0 THEN NOW() + INTERVAL '10 minutes' ELSE NULL END,
    scan_retry_count = CASE WHEN scan_interval_minutes > 0 THEN 1 ELSE 0 END,
    scan_lease_expires_at = NULL,
    last_scan_error_code = 'scan_protocol_upgrade'
WHERE scan_state = 'running' AND scan_attempt_id = '';
