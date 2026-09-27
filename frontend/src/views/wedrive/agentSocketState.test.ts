import assert from 'node:assert/strict'
import test from 'node:test'
import { agentReconnectDelay, displayedDeviceStatus, isScanRunning, shouldApplySocketClose, shouldReconnectAgent } from './agentSocketState.ts'

test('a stale socket close cannot mark a replacement connection offline', () => {
  const stale = {} as WebSocket
  const replacement = {} as WebSocket

  assert.equal(shouldApplySocketClose(replacement, stale), false)
  assert.equal(shouldApplySocketClose(replacement, replacement), true)
})

test('the active socket reconnects with bounded exponential backoff', () => {
  const active = {} as WebSocket

  assert.equal(shouldReconnectAgent(active, active, 'device-1', false), true)
  assert.equal(shouldReconnectAgent(active, {} as WebSocket, 'device-1', false), false)
  assert.equal(shouldReconnectAgent(active, active, '', false), false)
  assert.equal(shouldReconnectAgent(active, active, 'device-1', true), false)
  assert.deepEqual([0, 1, 2, 3, 4, 10].map(agentReconnectDelay), [1000, 2000, 4000, 8000, 15000, 15000])
})

test('the selected device label follows its live socket state', () => {
  assert.equal(displayedDeviceStatus('device-1', 'offline', 'device-1', 'online'), 'online')
  assert.equal(displayedDeviceStatus('device-1', 'online', 'device-1', 'offline'), 'offline')
  assert.equal(displayedDeviceStatus('device-2', 'online', 'device-1', 'offline'), 'online')
})

test('an expired scan lease does not keep the scan button disabled', () => {
  const now = Date.parse('2026-09-25T08:00:00Z')
  assert.equal(isScanRunning('running', '2026-09-25T08:01:00Z', now), true)
  assert.equal(isScanRunning('running', '2026-09-25T07:59:00Z', now), false)
  assert.equal(isScanRunning('running', undefined, now), false)
  assert.equal(isScanRunning('failed', '2026-09-25T08:01:00Z', now), false)
})
