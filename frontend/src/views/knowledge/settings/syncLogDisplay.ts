import type { SyncLog } from '@/api/datasource'

/** Correct historical partial logs whose inventory accounting shows every file failed. */
export function syncLogDisplayStatus(log: SyncLog): SyncLog['status'] {
  if (log.status !== 'partial') return log.status
  if (log.items_total <= 0 || log.items_failed < log.items_total) return log.status
  if (log.items_created || log.items_updated || log.items_deleted || log.items_skipped || log.result?.source_deferred) {
    return log.status
  }
  return 'failed'
}
