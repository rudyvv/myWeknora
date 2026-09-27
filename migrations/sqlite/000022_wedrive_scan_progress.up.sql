ALTER TABLE wedrive_sources ADD COLUMN scan_progress_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE wedrive_sources ADD COLUMN scan_last_progress_at DATETIME;
UPDATE wedrive_sources SET scan_last_progress_at = last_scan_started_at
    WHERE scan_state = 'running';
