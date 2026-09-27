import assert from 'node:assert/strict'
import test from 'node:test'
import type { SyncLog } from '@/api/datasource'
import { formatSyncLogError, syncLogDisplayStatus } from './syncLogDisplay'

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

test('the 63-failure WeDrive summary shows its safe reasons instead of a generic error', () => {
  const summary = [
    'Agent could not create some share links because the WeCom Drive sharing UI was unavailable (17 items)',
    "Agent could not create some share links; check the source user's share permission and tenant sharing policy (1 items)",
    'Some offline files do not have a usable share link (1 items)',
    'Some online documents could not be exported by the configured WeCom CLI identity (14 items)',
    'The WeCom CLI bot daily file-content retrieval quota has been reached; retry after the quota resets (1 items)',
    'Unsupported file formats are not supported (.rp: 1 item; .sql: 28 items)',
  ].join('; ')
  const displayed = formatSyncLogError(summary, 'wecom_drive_rpa')
  assert.match(displayed, /分享界面（17 项）/)
  assert.match(displayed, /分享权限和企业分享策略（1 项）/)
  assert.match(displayed, /分享链接（1 项）/)
  assert.match(displayed, /在线文档无法.*（14 项）/)
  assert.match(displayed, /每日文件内容读取配额已耗尽/)
  assert.match(displayed, /\.sql（28 项）/)
  assert.doesNotMatch(displayed, /请查看服务端日志/)
})
