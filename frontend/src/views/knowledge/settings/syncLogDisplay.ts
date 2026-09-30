import type { SyncLog } from '@/api/datasource'

/** Correct historical partial logs whose inventory accounting shows every file failed. */
export type SyncLogDisplayStatus = SyncLog['status'] | 'waiting_for_catch_up' | 'retry_wait'

export function syncLogDisplayStatus(log: SyncLog): SyncLogDisplayStatus {
  if (log.status === 'queued') {
    if (log.source_run_phase === 'waiting_for_catch_up') return 'waiting_for_catch_up'
    if (log.source_run_phase === 'retry_wait') return 'retry_wait'
    if (log.source_run_phase === 'failed') return 'failed'
  }
  if (log.status !== 'partial') return log.status
  if (log.items_total <= 0 || log.items_failed < log.items_total) return log.status
  if (log.items_created || log.items_updated || log.items_deleted || log.items_skipped || log.result?.source_deferred) {
    return log.status
  }
  return 'failed'
}

// WeDrive API responses expose only allowlisted aggregate reasons. Historical
// raw errors remain server-side; render the safe reason counts for operators.
export function formatSyncLogError(raw: string, dataSourceType?: string): string {
  const lower = raw.toLowerCase()
  const messages: string[] = []
  const count = (pattern: RegExp) => {
    const match = raw.match(pattern)
    return match ? `（${match[1]} 项）` : ''
  }
  if (lower.includes('offline file has no valid share link') || lower.includes('offline files do not have a usable share link')) {
    messages.push(`离线文件没有可用的分享链接${count(/offline files do not have a usable share link\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes('agent could not create some share links because the wecom drive sharing ui was unavailable')) {
    messages.push(`Agent 无法操作文件的微盘分享界面${count(/agent could not create some share links because the wecom drive sharing ui was unavailable\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes("agent could not create some share links; check the source user's share permission and tenant sharing policy")) {
    messages.push(`文件无法创建分享链接，请检查扫描账号的分享权限和企业分享策略${count(/agent could not create some share links; check the source user's share permission and tenant sharing policy\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes('agent could not create some share links because the wecom drive sharing operation failed')) {
    messages.push(`文件创建微盘分享链接失败${count(/agent could not create some share links because the wecom drive sharing operation failed\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes('online documents are not accessible to the configured wecom cli identity')) {
    messages.push(`在线文档未对当前企业微信 CLI 身份开放${count(/online documents are not accessible to the configured wecom cli identity\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes('offline file share links cannot be downloaded by the configured wecom cli identity')) {
    messages.push(`离线文件分享链接无法由当前企业微信 CLI 身份下载${count(/offline file share links cannot be downloaded by the configured wecom cli identity\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes('online documents could not be exported by the configured wecom cli identity')) {
    messages.push(`在线文档无法由当前企业微信 CLI 身份导出，请检查成员权限或 CLI 授权${count(/online documents could not be exported by the configured wecom cli identity\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes('offline files with a share link could not be downloaded by the configured wecom cli identity')) {
    messages.push(`离线文件虽有分享链接，仍无法由当前企业微信 CLI 身份下载${count(/offline files with a share link could not be downloaded by the configured wecom cli identity\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes('wecom cli bot daily file-content retrieval quota has been reached')) {
    messages.push('机器人每日文件内容读取配额已耗尽，请在配额恢复后重试')
  }
  if (lower.includes('wecom cli rate limit was reached')) {
    messages.push(`企业微信 CLI 请求触发限流，请稍后重试${count(/wecom cli rate limit was reached; retry later\s*\((\d+) items\)/i)}`)
  }
  if (lower.includes('wecom cli credential storage is unavailable') || lower.includes('create wecom cli profile') || lower.includes('credential initialization') || lower.includes('access is denied')) {
    messages.push('企业微信 CLI 凭据存储不可用')
  }
  if (lower.includes('wecom cli request timed out') || lower.includes('wecom-cli timed out')) {
    messages.push('企业微信 CLI 请求超时')
  }
  const unsupported = raw.match(/unsupported file formats are not supported\s*\(([^)]*)\)/i)
  if (unsupported) {
    const formats = unsupported[1]
      .split(';')
      .map(entry => entry.trim().replace(/:\s*(\d+) items?$/i, '（$1 项）'))
      .filter(Boolean)
      .join('、')
    messages.push(`不支持的文件格式：${formats}`)
  }
  if (lower.includes('some files could not be fetched from wecom drive')) {
    messages.push(`企业微信微盘文件暂时无法同步${count(/some files could not be fetched from wecom drive\s*\((\d+) items\)/i)}`)
  }
  if (messages.length > 0) return messages.join('；')
  if (lower.includes('fetch failed') || lower.includes('partial fetch')) {
    return '企业微信微盘文件暂时无法同步，请重试'
  }
  return dataSourceType === 'wecom_drive_rpa' ? '企业微信微盘同步失败，请查看服务端日志' : raw
}
