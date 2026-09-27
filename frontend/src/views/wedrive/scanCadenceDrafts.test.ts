import assert from 'node:assert/strict'
import test from 'node:test'
import { reconcileScanCadenceDrafts } from './scanCadenceDrafts.ts'

test('periodic refresh keeps an unsaved cadence choice but updates clean rows', () => {
  const drafts: Record<string, number> = { editing: 60, clean: 30, removed: 30 }
  reconcileScanCadenceDrafts(drafts,
    [{ id: 'editing', scan_interval_minutes: 30 }, { id: 'clean', scan_interval_minutes: 30 }, { id: 'removed', scan_interval_minutes: 30 }],
    [{ id: 'editing', scan_interval_minutes: 30 }, { id: 'clean', scan_interval_minutes: 360 }, { id: 'new', scan_interval_minutes: 0 }])
  assert.deepEqual(drafts, { editing: 60, clean: 360, new: 0 })
})

test('after the saved cadence reaches the server, later refreshes can update it', () => {
  const drafts: Record<string, number> = { source: 60 }
  reconcileScanCadenceDrafts(drafts,
    [{ id: 'source', scan_interval_minutes: 30 }],
    [{ id: 'source', scan_interval_minutes: 60 }])
  assert.equal(drafts.source, 60)
  reconcileScanCadenceDrafts(drafts,
    [{ id: 'source', scan_interval_minutes: 60 }],
    [{ id: 'source', scan_interval_minutes: 360 }])
  assert.equal(drafts.source, 360)
})
