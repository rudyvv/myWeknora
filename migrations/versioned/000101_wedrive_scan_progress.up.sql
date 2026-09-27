ALTER TABLE wedrive_sources
    ADD COLUMN scan_progress_seq BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN scan_last_progress_at TIMESTAMPTZ;

-- Retain currently valid attempts across deployment; their original claim
-- time is the progress baseline until the new tool reports actual progress.
UPDATE wedrive_sources SET scan_last_progress_at = last_scan_started_at
    WHERE scan_state = 'running';
