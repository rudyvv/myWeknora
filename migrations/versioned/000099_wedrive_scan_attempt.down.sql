-- The recovery of legacy running rows is intentionally not reversed.
ALTER TABLE wedrive_sources DROP COLUMN scan_attempt_id;
