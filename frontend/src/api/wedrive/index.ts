import { del, get, post, put } from '@/utils/request'

export interface WeDriveDevice {
  id: string
  name: string
  status: string
  agent_version: string
  last_seen_at?: string
}

export interface WeComCLIConnection {
  id: string
  name: string
  configured: boolean
  is_default: boolean
  status: string
  last_error?: string
}

export interface WeDriveSource {
  id: string
  knowledge_base_id: string
  connection_id?: string
  connection_name?: string
  device_id: string
  name: string
  root_url: string
  status: string
  auto_share: boolean
  sync_deletions: boolean
  scan_interval_minutes: number
  scan_state: 'idle' | 'waiting' | 'running' | 'retry_wait' | 'failed'
  scan_lease_expires_at?: string
  next_scan_at?: string
  last_scan_started_at?: string
  last_scan_error_code?: string
  last_complete_scan_at?: string
}

export const listWeDriveDevices = () => get('/api/v1/wedrive/devices')
export const createWeDriveRegistration = () => post('/api/v1/wedrive/device-registrations', {})
export const revokeWeDriveDevice = (id: string) => del(`/api/v1/wedrive/devices/${encodeURIComponent(id)}`)
export const createWeDriveEventTicket = (id: string) => post(`/api/v1/wedrive/devices/${encodeURIComponent(id)}/event-ticket`, {})

export const listWeDriveSources = (kbId = '') => get(`/api/v1/wedrive/sources${kbId ? `?kb_id=${encodeURIComponent(kbId)}` : ''}`)
export const createWeDriveSource = (kbId: string, payload: Record<string, unknown>) => post(`/api/v1/knowledge-bases/${encodeURIComponent(kbId)}/wedrive-sources`, payload)
export const approveWeDriveSource = (id: string, connectionId: string) => post(`/api/v1/wedrive/sources/${encodeURIComponent(id)}/approve`, { connection_id: connectionId })
export const rebindWeDriveSourceConnection = (id: string, connectionId: string) => put(`/api/v1/wedrive/sources/${encodeURIComponent(id)}/connection`, { connection_id: connectionId })
export const updateWeDriveSourceScanSettings = (id: string, scanIntervalMinutes: number) => put(`/api/v1/wedrive/sources/${encodeURIComponent(id)}/scan-settings`, { scan_interval_minutes: scanIntervalMinutes })
export const deleteWeDriveSource = (id: string) => del(`/api/v1/wedrive/sources/${encodeURIComponent(id)}`)

export const listWeComCLIConnections = () => get('/api/v1/wedrive/connections')
export const createWeComCLIConnection = (payload: Record<string, unknown>) => post('/api/v1/wedrive/connections', payload)
export const updateWeComCLIConnection = (id: string, payload: Record<string, unknown>) => put(`/api/v1/wedrive/connections/${encodeURIComponent(id)}`, payload)
export const deleteWeComCLIConnection = (id: string) => del(`/api/v1/wedrive/connections/${encodeURIComponent(id)}`)

export function weDriveEventURL(ticket: string): string {
  const base = new URL(import.meta.env.BASE_URL || '/', window.location.origin)
  base.protocol = base.protocol === 'https:' ? 'wss:' : 'ws:'
  base.pathname = `${base.pathname.replace(/\/$/, '')}/api/v1/wedrive/browser/events`
  base.search = new URLSearchParams({ ticket }).toString()
  return base.toString()
}
