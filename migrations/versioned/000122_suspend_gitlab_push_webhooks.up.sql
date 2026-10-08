-- GitLab Push Webhook: 已有实现，端到端验证未完成，暂不对外提供。
-- Preserve credentials/receipts for audit, but disable every existing config.
UPDATE source_gitlab_webhook_configs
SET enabled = FALSE, updated_at = now()
WHERE enabled = TRUE;

-- Fence only active webhook work. Keep unrelated manual/scheduled pending work
-- and the last successfully published source/Wiki snapshots intact.
UPDATE source_sync_states AS state
SET fencing_token = state.fencing_token + 1,
    active_sync_log_id = NULL, lease_owner = NULL, lease_expires_at = NULL,
    updated_at = now()
WHERE EXISTS (
    SELECT 1 FROM source_sync_runs AS run
    WHERE run.sync_log_id = state.active_sync_log_id
      AND run.trigger = 'gitlab_webhook'
);

UPDATE source_sync_states AS state
SET pending_sync_log_id = NULL, pending_trigger = '', updated_at = now()
WHERE state.pending_trigger = 'gitlab_webhook'
   OR EXISTS (
    SELECT 1 FROM source_sync_runs AS run
    WHERE run.sync_log_id = state.pending_sync_log_id
      AND run.trigger = 'gitlab_webhook'
);

UPDATE sync_logs AS log
SET status = 'canceled', finished_at = now(), updated_at = now(),
    error_message = 'GitLab Push Webhook is temporarily unavailable; use manual or scheduled sync.'
WHERE log.status IN ('queued', 'running')
  AND EXISTS (
    SELECT 1 FROM source_sync_runs AS run
    WHERE run.sync_log_id = log.id AND run.trigger = 'gitlab_webhook'
);

UPDATE source_sync_runs
SET phase = 'canceled', updated_at = now()
WHERE trigger = 'gitlab_webhook'
  AND sync_log_id IN (
    SELECT id FROM sync_logs
    WHERE status = 'canceled'
      AND error_message = 'GitLab Push Webhook is temporarily unavailable; use manual or scheduled sync.'
  );
