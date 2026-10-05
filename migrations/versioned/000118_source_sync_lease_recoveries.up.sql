ALTER TABLE source_sync_runs
    ADD COLUMN lease_recovery_count BIGINT NOT NULL DEFAULT 0
        CHECK (lease_recovery_count >= 0);
