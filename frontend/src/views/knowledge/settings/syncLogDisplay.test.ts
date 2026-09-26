import assert from 'node:assert/strict'
import test from 'node:test'
import type { SyncLog } from '@/api/datasource'
import { syncLogDisplayStatus } from './syncLogDisplay'

function log(overrides: Partial<SyncLog>): SyncLog {
  return {
    id: 'log-1', data_source_id: 'source-1', status: 'partial',
    started_at: '', finished_at: '', items_total: 11, items_created: 0,
    items_updated: 0, items_deleted: 0, items_skipped: 0, items_failed: 11,
    error_message: '', ...overrides,
  }
}

test('historical all-failed inventory log displays failed', () => {
  assert.equal(syncLogDisplayStatus(log({})), 'failed')
})

test('mixed and deferred inventory logs remain partial', () => {
  assert.equal(syncLogDisplayStatus(log({ items_failed: 10, items_skipped: 1 })), 'partial')
  assert.equal(syncLogDisplayStatus(log({ result: { source_deferred: 1 } })), 'partial')
})

test('a successful duplicate-only run remains successful', () => {
  assert.equal(syncLogDisplayStatus(log({ status: 'success', items_failed: 0, items_skipped: 11 })), 'success')
})
