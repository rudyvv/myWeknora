export function shouldApplySocketClose(current: WebSocket | null, closing: WebSocket): boolean {
  return current === closing
}

export function shouldReconnectAgent(current: WebSocket | null, closing: WebSocket, deviceID: string, disposed: boolean): boolean {
  return !disposed && Boolean(deviceID) && shouldApplySocketClose(current, closing)
}

export function agentReconnectDelay(attempt: number): number {
  return Math.min(1000 * (2 ** Math.max(0, attempt)), 15000)
}

export function displayedDeviceStatus(deviceID: string, listedStatus: string, selectedDeviceID: string, agentState: string): string {
  return deviceID === selectedDeviceID ? agentState : listedStatus
}

export function isScanRunning(scanState: string, leaseExpiresAt?: string, now = Date.now()): boolean {
  if (scanState !== 'running') return false
  const expiry = leaseExpiresAt ? Date.parse(leaseExpiresAt) : NaN
  return Number.isFinite(expiry) && expiry > now
}
