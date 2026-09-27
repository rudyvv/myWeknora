<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { useRoute } from 'vue-router'
import { listKnowledgeBases } from '@/api/knowledge-base'
import { useAuthStore } from '@/stores/auth'
import { agentReconnectDelay, displayedDeviceStatus, isScanRunning, shouldApplySocketClose, shouldReconnectAgent } from './agentSocketState'
import {
  approveWeDriveSource,
  createWeComCLIConnection,
  createWeDriveEventTicket,
  createWeDriveRegistration,
  createWeDriveSource,
	deleteWeDriveSource,
  deleteWeComCLIConnection,
  listWeComCLIConnections,
  listWeDriveDevices,
  listWeDriveSources,
  rebindWeDriveSourceConnection,
  updateWeComCLIConnection,
  updateWeDriveSourceScanSettings,
  weDriveEventURL,
  type WeComCLIConnection,
  type WeDriveDevice,
  type WeDriveSource,
} from '@/api/wedrive'

const route = useRoute()
const auth = useAuthStore()
// The backend treats both Owner and Admin as administrators for WeDrive
// sources. Keep the client-side prerequisite check aligned with it.
const isAdmin = computed(() => auth.hasRole('admin') || auth.hasRole('owner'))
const isOwner = computed(() => auth.hasRole('owner'))
const devices = ref<WeDriveDevice[]>([])
const sources = ref<WeDriveSource[]>([])
const connections = ref<WeComCLIConnection[]>([])
const knowledgeBases = ref<any[]>([])
const registration = ref<{ code: string; expires_at: string } | null>(null)
const selectedDeviceID = ref('')
const agentState = ref('offline')
const loginQR = ref('')
const scanText = ref('')
const selectedFolderName = ref('')
const showManualRootURL = ref(false)
const socket = ref<WebSocket | null>(null)
const sourceForm = reactive({ knowledge_base_id: String(route.query.kb_id || ''), device_id: '', name: '', root_url: '', auto_share: true, sync_deletions: false, scan_interval_minutes: 0 })
const connectionForm = reactive({ name: '企业微信资料管理员', bot_id: '', secret: '', is_default: true })
const approving = reactive<Record<string, string>>({})
const rebinding = reactive<Record<string, string>>({})
const connectionEdits = reactive<Record<string, { bot_id: string; secret: string }>>({})
const scanIntervalEdits = reactive<Record<string, number>>({})
let refreshTimer: number | undefined
let reconnectTimer: number | undefined
let reconnectAttempt = 0
let disposed = false
let refreshVersion = 0

const agentReleaseVersion = '0.4.2'
const agentDownloadName = `WeKnora-WeDrive-Tool-${agentReleaseVersion}.exe`
const agentDownloadURL = String(import.meta.env.VITE_WEDRIVE_AGENT_DOWNLOAD_URL || `${String(import.meta.env.BASE_URL || '/').replace(/\/?$/, '/')}downloads/WeKnora-WeDrive-Tool.exe?v=${agentReleaseVersion}`)
const agentServerURL = window.location.origin

function supportsScanAttempts(version?: string): boolean {
  const raw = String(version || '').trim()
  if (raw.length > 32) return false
  const match = raw.replace(/^v/, '').match(/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/)
  if (!match) return false
  const major = Number(match[1])
  const minor = Number(match[2])
  return major > 0 || (major === 0 && minor >= 4)
}

function sourceAgentSupportsAttempts(source: WeDriveSource): boolean {
  return supportsScanAttempts(devices.value.find(device => device.id === source.device_id)?.agent_version)
}

function payload<T>(value: any): T { return (value?.data ?? value) as T }

async function refresh() {
  const version = ++refreshVersion
  const admin = isAdmin.value
  const [deviceRes, sourceRes, kbRes, connectionRes] = await Promise.all([
    listWeDriveDevices(),
    listWeDriveSources(),
    listKnowledgeBases(),
    admin ? listWeComCLIConnections() : Promise.resolve([]),
  ])
  if (disposed || version !== refreshVersion) return
  devices.value = payload<WeDriveDevice[]>(deviceRes) || []
  connections.value = admin ? payload<WeComCLIConnection[]>(connectionRes) || [] : []
  sources.value = payload<WeDriveSource[]>(sourceRes) || []
  knowledgeBases.value = payload<any[]>(kbRes) || []
  if (!selectedDeviceID.value && devices.value[0]) selectedDeviceID.value = devices.value[0].id
  if (!sourceForm.device_id && devices.value[0]) sourceForm.device_id = devices.value[0].id
  if (!sourceForm.knowledge_base_id && knowledgeBases.value[0]) sourceForm.knowledge_base_id = knowledgeBases.value[0].id
  if (admin) {
    for (const connection of connections.value) connectionEdits[connection.id] ||= { bot_id: '', secret: '' }
    for (const source of sources.value) {
      approving[source.id] = source.connection_id || connections.value.find(c => c.is_default)?.id || ''
      rebinding[source.id] = source.connection_id || ''
    }
  }
  for (const source of sources.value) scanIntervalEdits[source.id] = source.scan_interval_minutes
}

async function createRegistration() {
  registration.value = payload(await createWeDriveRegistration())
}

function scheduleAgentReconnect() {
  if (disposed || !selectedDeviceID.value || reconnectTimer !== undefined) return
  const delay = agentReconnectDelay(reconnectAttempt++)
  reconnectTimer = window.setTimeout(() => {
    reconnectTimer = undefined
    void openAgentSocket()
  }, delay)
}

async function openAgentSocket() {
  if (reconnectTimer !== undefined) {
    window.clearTimeout(reconnectTimer)
    reconnectTimer = undefined
  }
  const previous = socket.value
  socket.value = null
  previous?.close()
  agentState.value = 'offline'
  if (!selectedDeviceID.value) return
  let result: { ticket: string }
  try {
    result = payload<{ ticket: string }>(await createWeDriveEventTicket(selectedDeviceID.value))
  } catch {
    scheduleAgentReconnect()
    return
  }
  const ws = new WebSocket(weDriveEventURL(result.ticket))
  socket.value = ws
  ws.onopen = () => { reconnectAttempt = 0 }
  ws.onmessage = event => {
    if (!shouldApplySocketClose(socket.value, ws)) return
    const frame = JSON.parse(event.data)
    if (frame.type === 'status') {
      agentState.value = frame.data?.state || 'offline'
      void refresh()
    }
    if (frame.type === 'login_qr') loginQR.value = frame.data?.image || ''
    if (frame.type === 'login_result' && frame.data?.ok) { loginQR.value = ''; MessagePlugin.success('企业微信登录成功') }
    if (frame.type === 'login_result' && ['login_queued', 'login_starting', 'folder_browser_ready', 'login_browser_opened'].includes(frame.data?.reason)) MessagePlugin.info(frame.data?.message || '请在同步工具打开的微盘中进入目标文件夹后再次读取')
    if (frame.type === 'folder_selected') {
      sourceForm.root_url = frame.data?.root_url || ''
      selectedFolderName.value = frame.data?.name || '已选择微盘目录'
      if (!sourceForm.name && frame.data?.name) sourceForm.name = frame.data.name
      MessagePlugin.success('已读取同步工具当前打开的微盘文件夹')
    }
    if (frame.type === 'scan_progress') scanText.value = frame.data?.message || ''
    if (frame.type === 'error') { MessagePlugin.error(agentErrorMessage(frame.data?.reason, frame.data?.message)); void refresh() }
    if (frame.type === 'scan_result') { scanText.value = '目录清单已提交'; void refresh() }
  }
  ws.onclose = () => {
    const reconnect = shouldReconnectAgent(socket.value, ws, selectedDeviceID.value, disposed)
    if (!shouldApplySocketClose(socket.value, ws)) return
    socket.value = null
    agentState.value = 'offline'
    if (reconnect) scheduleAgentReconnect()
  }
}

async function connectAgent() {
  reconnectAttempt = 0
  await openAgentSocket()
}

function sendCommand(type: 'login' | 'select_folder' | 'scan' | 'cancel' | 'shutdown', data: Record<string, unknown> = {}) {
  if (socket.value?.readyState !== WebSocket.OPEN) {
    MessagePlugin.warning('请先连接在线的本机同步工具')
    return
  }
  socket.value.send(JSON.stringify({ type, data }))
}

function agentErrorMessage(reason: string | undefined, fallback: string | undefined): string {
  if (reason === 'space_home') return '同步工具当前位于“共享空间”首页。请在其打开的 Edge 窗口中进入目标文件夹后再读取。'
  if (reason === 'folder_not_selected') return '同步工具已打开微盘，但当前不是文件夹页。请进入目标文件夹后再读取。'
  if (reason === 'not_wedrive') return '同步工具当前没有打开企业微信微盘页面。请先点击“登录企业微信”。'
  if (reason === 'listing_timeout') return '已打开目标目录，但同步工具没有捕获到目录列表。请在其 Edge 窗口刷新该目录，确认列表显示后重试。'
  if (reason === 'tree_walk_failed') return '已读取根目录，但递归进入某个子目录失败。请保持同步工具的微盘窗口登录并再次扫描。'
  return fallback || '本机同步工具执行失败'
}

function stopAgent() {
  sendCommand('shutdown')
  MessagePlugin.info('已请求停止本机同步工具；设备注册和企业微信登录资料会保留。')
}

function loginAgent() {
  sendCommand('login')
}

async function submitSource() {
  if (!sourceForm.knowledge_base_id || !sourceForm.device_id || !sourceForm.name.trim() || !sourceForm.root_url.trim()) {
    MessagePlugin.warning('请完整填写知识库、同步工具和名称，并读取当前微盘文件夹')
    return
  }
  if (!isWeDriveFolderURL(sourceForm.root_url)) {
    MessagePlugin.warning('当前目录定位无效，请在同步工具打开的浏览器中进入目标文件夹后重新读取')
    return
  }
  const device = devices.value.find(item => item.id === sourceForm.device_id)
  if (!supportsScanAttempts(device?.agent_version)) {
    MessagePlugin.warning('此同步工具版本不支持扫描尝试协议。请升级至 0.4.0 或更高版本后再创建同步源。')
    return
  }
  if (isAdmin.value && !connections.value.length) {
    MessagePlugin.warning('尚未配置可用的企业微信 CLI 凭据。请先保存 BotID 和 Secret。')
    return
  }
  const scanInterval = Number(sourceForm.scan_interval_minutes)
  if (!Number.isInteger(scanInterval) || !scanIntervalOptions.some(option => option.value === scanInterval)) {
    MessagePlugin.warning('请选择有效的目录扫描频率')
    return
  }
  let createdSource: WeDriveSource | null = null
  try {
    createdSource = payload<WeDriveSource>(await createWeDriveSource(sourceForm.knowledge_base_id, { ...sourceForm, scan_interval_minutes: scanInterval }))
  } catch (error: any) {
    const message = error?.response?.data?.message || error?.message || '请检查 CLI 连接和当前账号权限'
    MessagePlugin.error(`同步源未创建：${message}`)
    return
  }
  MessagePlugin.success(isAdmin.value
    ? `同步源已创建，目录扫描：${scanIntervalLabel(createdSource?.scan_interval_minutes ?? scanInterval)}`
    : '同步申请已提交，等待管理员批准')
  sourceForm.name = ''
  sourceForm.root_url = ''
  selectedFolderName.value = ''
  showManualRootURL.value = false
  await refresh()
}

function isWeDriveFolderURL(value: string): boolean {
  try {
    const url = new URL(value.trim())
    return url.protocol === 'https:' && url.hostname === 'drive.weixin.qq.com' && (
      url.hash.startsWith('#/webdisk/') ||
      (url.pathname.replace(/\/$/, '') === '/webdisk/index' &&
        url.hash.startsWith('#/cgi/ssr/space/') && url.hash.includes('folderid='))
    )
  } catch {
    return false
  }
}

async function saveConnection() {
  await createWeComCLIConnection({ ...connectionForm })
  connectionForm.secret = ''
  MessagePlugin.success('CLI 连接已保存')
  await refresh()
}

async function setDefaultConnection(connection: WeComCLIConnection) {
  await updateWeComCLIConnection(connection.id, { name: connection.name, is_default: true })
  MessagePlugin.success('已设为默认 CLI 连接')
  await refresh()
}

async function updateConnectionCredentials(connection: WeComCLIConnection) {
  const edit = connectionEdits[connection.id]
  if (!edit?.bot_id.trim() || !edit.secret.trim()) return MessagePlugin.warning('更新时需要同时填写 BotID 和 Secret')
  await updateWeComCLIConnection(connection.id, { name: connection.name, bot_id: edit.bot_id, secret: edit.secret, is_default: connection.is_default })
  edit.bot_id = ''
  edit.secret = ''
  MessagePlugin.success('CLI 凭据已更新；使用该连接的同步源会在下一次内容同步时使用新凭据')
  await refresh()
}

function boundSourceCount(connection: WeComCLIConnection): number {
  return sources.value.filter(source => source.connection_id === connection.id).length
}

function removeConnection(connection: WeComCLIConnection) {
  const count = boundSourceCount(connection)
  if (count > 0) {
    MessagePlugin.warning(`“${connection.name}”仍被 ${count} 个同步源使用；请先删除这些同步源后再删除凭据。已入库的文档不会自动删除。`)
    return
  }
  const dialog = DialogPlugin.confirm({
    header: '删除企业微信 CLI 凭据？',
    body: `将删除“${connection.name}”保存的 BotID 与 Secret。此操作不能恢复。`,
    confirmBtn: '删除凭据',
    theme: 'warning',
    onConfirm: async () => {
      try {
        await deleteWeComCLIConnection(connection.id)
        MessagePlugin.success('企业微信 CLI 凭据已删除')
        await refresh()
      } catch (error: any) {
        MessagePlugin.error(error?.response?.data?.error || error?.message || '删除 CLI 凭据失败')
      } finally {
        dialog.hide()
      }
    },
    onCancel: () => dialog.hide(),
  })
}

async function approve(source: WeDriveSource) {
  const connectionID = approving[source.id]
  if (!connectionID) return MessagePlugin.warning('请选择 CLI 连接')
  await approveWeDriveSource(source.id, connectionID)
  MessagePlugin.success('同步源已批准')
  await refresh()
}

function connectionLabel(source: WeDriveSource): string {
  if (!source.connection_id) return source.status === 'pending' ? '待管理员绑定' : '未绑定'
  return source.connection_name || connections.value.find(connection => connection.id === source.connection_id)?.name || '已绑定的 CLI 凭据'
}

function knowledgeBaseLabel(source: WeDriveSource): string {
  return knowledgeBases.value.find(kb => kb.id === source.knowledge_base_id)?.name || `未知知识库（ID：${source.knowledge_base_id}）`
}

function deviceLabel(device: WeDriveDevice): string {
  const status = displayedDeviceStatus(device.id, device.status, selectedDeviceID.value, agentState.value)
  return `${device.name}（${status === 'online' ? '在线' : '离线'}）`
}

function sourceStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    pending: '待管理员批准',
    awaiting_inventory: '等待首次扫描',
    active: '已启用',
    paused: '已暂停',
    rejected: '已拒绝',
    error: '异常',
  }
  return labels[status] || '状态未知'
}

function connectionStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    unknown: '尚未检测',
    healthy: '可用',
    error: '异常',
  }
  return labels[status] || '状态未知'
}

function rebindSource(source: WeDriveSource) {
  const connectionID = rebinding[source.id]
  if (!connectionID) return MessagePlugin.warning('请选择要使用的 CLI 凭据')
  if (connectionID === source.connection_id) return
  const target = connections.value.find(connection => connection.id === connectionID)
  if (!target) return MessagePlugin.warning('所选 CLI 凭据已不存在，请刷新页面后重试')
  const dialog = DialogPlugin.confirm({
    header: '更换同步源的 CLI 凭据？',
    body: `“${source.name}”将改用“${target.name}”。不会自动扫描或重新入库；下一次内容同步将使用新凭据。`,
    confirmBtn: '确认更换',
    onConfirm: async () => {
      try {
        await rebindWeDriveSourceConnection(source.id, connectionID)
        MessagePlugin.success(`已将“${source.name}”改绑到“${target.name}”`)
        await refresh()
      } catch (error: any) {
        const message = error?.response?.data?.error || error?.message || ''
        MessagePlugin.error(message.includes('content sync is currently running') ? '当前正在进行内容同步，请完成后再更换凭据。' : (message || '更换 CLI 凭据失败'))
      } finally {
        dialog.hide()
      }
    },
    onCancel: () => dialog.hide(),
  })
}

const scanIntervalOptions = [
  { label: '仅手动', value: 0 },
  { label: '每 30 分钟', value: 30 },
  { label: '每 1 小时', value: 60 },
  { label: '每 6 小时', value: 360 },
  { label: '每天', value: 1440 },
]

function scanIntervalLabel(value: number): string {
  return scanIntervalOptions.find(item => item.value === value)?.label || '每 30 分钟'
}

function scanStateLabel(source: WeDriveSource): string {
  if (source.scan_state === 'running') return isScanRunning(source.scan_state, source.scan_lease_expires_at) ? '扫描中' : '上次扫描已超时，可重新扫描'
  if (source.scan_state === 'retry_wait') return '将在 10 分钟后重试'
  if (source.scan_state === 'failed' && source.last_scan_error_code === 'scan_interrupted') return '上次扫描因服务重启中断，可重新扫描'
  if (source.scan_state === 'failed') return source.next_scan_at ? '上次扫描失败，等待下次计划' : '上次扫描失败'
  if (source.scan_interval_minutes === 0) return '仅手动扫描'
  return source.next_scan_at ? `下次扫描：${new Date(source.next_scan_at).toLocaleString()}` : '等待下一次扫描'
}

function scanHistoryLabel(source: WeDriveSource): string {
  const last = source.last_complete_scan_at ? `最近成功：${new Date(source.last_complete_scan_at).toLocaleString()}` : '尚无完整扫描'
  return source.last_scan_error_code ? `${last} · 最近错误：${source.last_scan_error_code}` : last
}

async function saveScanInterval(source: WeDriveSource) {
  const interval = scanIntervalEdits[source.id]
  if (!Number.isInteger(interval)) return MessagePlugin.warning('请选择有效的目录扫描频率')
  await updateWeDriveSourceScanSettings(source.id, interval)
  MessagePlugin.success(`目录扫描频率已设为“${scanIntervalLabel(interval)}”`)
  await refresh()
}

function removeSource(source: WeDriveSource) {
  const dialog = DialogPlugin.confirm({
    header: '删除微盘同步源？',
    body: `将停止“${source.name}”的目录扫描和后续 CLI 同步。已入库的知识不会自动删除。`,
    confirmBtn: '删除同步源',
    theme: 'warning',
    onConfirm: async () => {
      try {
        await deleteWeDriveSource(source.id)
        MessagePlugin.success('同步源已删除，后续不再扫描或同步')
        await refresh()
      } catch (error: any) {
        MessagePlugin.error(error?.response?.data?.error || error?.message || '删除同步源失败')
      } finally {
        dialog.hide()
      }
    },
    onCancel: () => dialog.hide(),
  })
}

onMounted(async () => {
  await refresh()
  if (selectedDeviceID.value) await connectAgent()
  refreshTimer = window.setInterval(async () => {
    const hadDevice = Boolean(selectedDeviceID.value)
    await refresh()
    if (!hadDevice && selectedDeviceID.value) await connectAgent()
  }, 15000)
})
onBeforeUnmount(() => {
  disposed = true
  socket.value?.close()
  if (refreshTimer) window.clearInterval(refreshTimer)
  if (reconnectTimer !== undefined) window.clearTimeout(reconnectTimer)
})
</script>

<template>
  <main class="wedrive-page">
    <header>
      <div><h1>企业微信微盘同步</h1><p>本机同步工具读取目录结构；服务端通过企业微信 CLI 获取文件内容并入库。</p></div>
      <t-tag :theme="agentState === 'online' ? 'success' : 'default'" variant="light">本机同步工具{{ agentState === 'online' ? '在线' : '离线' }}</t-tag>
    </header>

    <section class="card">
      <h2>1. 连接本机同步工具</h2>
      <div class="agent-install">
        <div><strong>首次使用需要下载Windows同步工具</strong></div>
        <a class="agent-download-link" :href="agentDownloadURL" :download="agentDownloadName">
          <t-button variant="outline"><template #icon><t-icon name="download" /></template>下载Windows同步工具 {{ agentReleaseVersion }}</t-button>
        </a>
      </div>
      <ol class="agent-steps">
        <li>下载并运行同步工具。</li>
        <li>点击“注册新设备”，将页面显示的服务地址和一次性注册码填入同步工具。</li>
        <li>登录企业微信。</li>
      </ol>
      <div class="row">
        <t-select v-model="selectedDeviceID" placeholder="选择已注册设备" @change="connectAgent">
          <t-option v-for="device in devices" :key="device.id" :value="device.id" :label="deviceLabel(device)" />
        </t-select>
        <t-button variant="outline" @click="createRegistration">注册新设备</t-button>
        <t-button :disabled="!selectedDeviceID" @click="connectAgent">重新检测连接</t-button>
        <t-button theme="primary" :disabled="agentState !== 'online'" @click="loginAgent">登录企业微信</t-button>
        <t-button theme="danger" variant="outline" :disabled="agentState !== 'online'" @click="stopAgent">停止本机同步工具</t-button>
      </div>
      <div v-if="registration" class="registration">
        <strong>WeKnora 服务地址</strong><code>{{ agentServerURL }}</code>
        <strong>一次性注册码（10 分钟有效）</strong><code>{{ registration.code }}</code>
        <span>在 Windows 同步工具首次启动窗口中依次粘贴以上两项；设备私钥只保存在当前 Windows 用户的 DPAPI 中。</span>
      </div>
      <div v-if="loginQR" class="qr"><img :src="loginQR" alt="企业微信登录二维码"><span>请用企业微信扫码登录。二维码不会写入数据库。</span></div>
      <p v-if="scanText" class="muted">{{ scanText }}</p>
    </section>

    <section v-if="isOwner" class="card">
      <h2>2. 配置企业微信 CLI 凭据</h2>
      <p class="muted">首次使用时保存一组企业微信CLI凭据。多个同步源可复用同一组凭据。</p>
      <div class="form-grid connection"><label>连接名称<t-input v-model="connectionForm.name" /></label><label>BotID<t-input v-model="connectionForm.bot_id" /></label><label>Secret<t-input v-model="connectionForm.secret" type="password" /></label></div>
      <t-button @click="saveConnection">保存 CLI 连接</t-button>
      <div v-for="connection in connections" :key="connection.id" class="connection-row">
        <div><strong>{{ connection.name }}</strong><p>{{ connection.configured ? '凭据已配置' : '凭据未配置' }} · {{ connectionStatusLabel(connection.status) }}<span v-if="connection.is_default"> · 默认连接</span></p></div>
        <t-input v-model="connectionEdits[connection.id].bot_id" placeholder="新 BotID" />
        <t-input v-model="connectionEdits[connection.id].secret" type="password" placeholder="新 Secret" />
        <t-button size="small" variant="outline" :disabled="connection.is_default" @click="setDefaultConnection(connection)">设为默认</t-button>
        <t-button size="small" @click="updateConnectionCredentials(connection)">更新凭据</t-button>
        <t-button size="small" theme="danger" variant="outline" @click="removeConnection(connection)">删除</t-button>
      </div>
    </section>

    <section class="card">
      <h2>{{ isOwner ? '3.' : '2.' }} 选择微盘目录并创建同步源</h2>
      <p class="muted">在本机同步工具打开的企业微信微盘中进入目标文件夹，点击读取当前文件夹后创建同步源。</p>
      <div class="form-grid">
        <label>目标知识库<t-select v-model="sourceForm.knowledge_base_id"><t-option v-for="kb in knowledgeBases" :key="kb.id" :value="kb.id" :label="kb.name" /></t-select></label>
        <label>本机同步工具<t-select v-model="sourceForm.device_id"><t-option v-for="device in devices" :key="device.id" :value="device.id" :label="deviceLabel(device)" /></t-select></label>
        <label>数据源名称<t-input v-model="sourceForm.name" placeholder="例如：项目 A 交付文档" /></label>
		<label v-if="isAdmin">目录扫描频率<t-select v-model="sourceForm.scan_interval_minutes"><t-option v-for="option in scanIntervalOptions" :key="option.value" :value="option.value" :label="option.label" /></t-select></label>
		<label v-else>目录扫描频率<span class="field-help">由管理员配置；新申请默认仅手动扫描。</span></label>
        <div class="wide folder-selection">
          <span class="field-label">同步根目录</span>
          <div class="folder-selection-panel">
            <div>
              <strong>{{ sourceForm.root_url ? (selectedFolderName || '已选择微盘目录') : '尚未选择微盘目录' }}</strong>
              <p>{{ sourceForm.root_url ? '目录已读取，可以创建同步源。' : '先在本机同步工具打开的企业微信微盘中进入目标文件夹。' }}</p>
            </div>
            <t-button variant="outline" :disabled="agentState !== 'online'" @click="sendCommand('select_folder')">读取当前文件夹</t-button>
          </div>
          <button type="button" class="advanced-toggle" @click="showManualRootURL = !showManualRootURL">{{ showManualRootURL ? '收起高级设置' : '高级：手工填写目录地址' }}</button>
          <div v-if="showManualRootURL" class="manual-folder-url">
            <t-input v-model="sourceForm.root_url" placeholder="https://drive.weixin.qq.com/#/webdisk/..." />
            <span class="field-help">请填写同步工具浏览器中的文件夹页面地址。</span>
          </div>
        </div>
      </div>
      <div class="row switches"><t-switch v-model="sourceForm.auto_share" /><span>允许自动创建缺失的分享链接（默认开启）</span><t-switch v-model="sourceForm.sync_deletions" /><span>同步删除（两次完整清单且超过 24 小时后生效）</span></div>
	  <p v-if="sourceForm.device_id && !supportsScanAttempts(devices.find(device => device.id === sourceForm.device_id)?.agent_version)" class="cadence-warning">该同步工具版本过旧；请升级到 0.4.0 或更高版本后扫描。</p>
      <t-button theme="primary" @click="submitSource">创建同步源</t-button>
    </section>

    <section class="card">
      <h2>同步源</h2>
      <div v-if="!sources.length" class="empty">暂无同步源</div>
      <div v-for="source in sources" :key="source.id" class="source-row">
        <div class="source-summary">
          <div class="source-details"><div class="source-title"><strong>{{ source.name }}</strong><t-tag size="small">{{ sourceStatusLabel(source.status) }}</t-tag></div><p>目标知识库：{{ knowledgeBaseLabel(source) }}<br>CLI 凭据：{{ connectionLabel(source) }}<br>同步目录由绑定的本机同步工具管理 · {{ scanStateLabel(source) }}<br>{{ scanHistoryLabel(source) }}</p></div>
        </div>
        <div v-if="isAdmin" class="source-actions">
          <div v-if="source.status === 'pending'" class="source-action-group">
            <span class="source-action-label">绑定 CLI 凭据</span>
            <div class="source-action-controls"><t-select v-model="approving[source.id]" size="small" class="source-connection" placeholder="选择 CLI 连接"><t-option v-for="connection in connections" :key="connection.id" :value="connection.id" :label="connection.name" :disabled="!connection.configured" /></t-select><t-button size="small" @click="approve(source)">批准并绑定</t-button></div>
          </div>
          <div v-if="source.status === 'awaiting_inventory' || source.status === 'active'" class="source-action-group">
            <span class="source-action-label">更换 CLI 凭据</span>
            <div class="source-action-controls"><t-select v-model="rebinding[source.id]" size="small" class="source-connection" aria-label="选择 CLI 凭据"><t-option v-for="connection in connections" :key="connection.id" :value="connection.id" :label="connection.name" :disabled="!connection.configured" /></t-select><t-button size="small" variant="outline" :disabled="!rebinding[source.id] || rebinding[source.id] === source.connection_id" @click="rebindSource(source)">更换凭据</t-button></div>
          </div>
          <div v-if="sourceAgentSupportsAttempts(source) && (source.status === 'awaiting_inventory' || source.status === 'active')" class="source-action-group">
            <span class="source-action-label">目录扫描</span>
            <div class="source-action-controls"><t-select v-model="scanIntervalEdits[source.id]" size="small" class="scan-interval"><t-option v-for="option in scanIntervalOptions" :key="option.value" :value="option.value" :label="option.label" /></t-select><t-button size="small" variant="outline" :disabled="!isAdmin || scanIntervalEdits[source.id] === source.scan_interval_minutes" @click="saveScanInterval(source)">保存频率</t-button><t-button size="small" variant="outline" :disabled="isScanRunning(source.scan_state, source.scan_lease_expires_at)" @click="sendCommand('scan', { source_id: source.id })">立即扫描</t-button></div>
          </div>
          <span v-else-if="source.status === 'awaiting_inventory' || source.status === 'active'" class="agent-upgrade-hint">需升级同步工具至 0.4.0+</span>
          <div class="source-action-group source-danger"><span class="source-action-label">同步源</span><t-button size="small" theme="danger" variant="outline" @click="removeSource(source)">删除</t-button></div>
        </div>
      </div>
    </section>
  </main>
</template>

<style scoped>
.wedrive-page{width:100%;height:100%;min-height:0;overflow-y:auto;box-sizing:border-box;padding:32px;max-width:1180px;margin:0 auto;color:var(--td-text-color-primary)}
header{display:flex;align-items:flex-start;justify-content:space-between;margin-bottom:24px}h1{font-size:28px;margin:0 0 8px}header p,.muted{color:var(--td-text-color-secondary)}
.card{background:var(--td-bg-color-container);border:1px solid var(--td-component-border);border-radius:12px;padding:22px;margin-bottom:18px}.card h2{font-size:17px;margin:0 0 16px}
.row{display:flex;gap:12px;align-items:center;flex-wrap:wrap}.row>.t-select{min-width:260px}.agent-install{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:14px;margin-bottom:12px;background:var(--td-bg-color-secondarycontainer);border-radius:8px}.agent-install>div{display:flex;flex-direction:column;gap:4px}.agent-install span,.agent-steps{color:var(--td-text-color-secondary);font-size:13px}.agent-download-link{flex:none;text-decoration:none}.agent-steps{margin:0 0 16px;padding-left:20px;line-height:1.8}.registration{margin-top:16px;padding:14px;background:var(--td-bg-color-secondarycontainer);border-radius:8px;display:flex;gap:10px;flex-direction:column}.registration code{font-size:16px;user-select:all}.qr{display:flex;align-items:center;gap:18px;margin-top:16px}.qr img{width:220px;height:220px;object-fit:contain;border:1px solid var(--td-component-border)}
.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-bottom:16px}.form-grid label{display:flex;flex-direction:column;gap:7px;font-size:13px}.form-grid .wide{grid-column:1/-1}.connection{grid-template-columns:1fr 1fr 1fr}.switches{margin:12px 0 18px}.source-row{display:flex;flex-direction:column;gap:12px;padding:18px 0;border-top:1px solid var(--td-component-stroke)}.source-row:first-of-type{border-top:0}.source-details{min-width:0}.source-title{display:flex;align-items:center;gap:8px}.source-row p{margin:5px 0 0;color:var(--td-text-color-secondary);font-size:12px;line-height:1.6;word-break:break-all}.source-actions{display:grid;grid-template-columns:minmax(280px,360px) minmax(340px,440px) auto;align-items:end;justify-content:start;gap:18px;width:100%;padding:12px 14px;background:var(--td-bg-color-secondarycontainer);border:1px solid var(--td-component-border);border-radius:8px;box-sizing:border-box}.source-action-group{display:flex;flex-direction:column;gap:6px;min-width:0}.source-action-label{color:var(--td-text-color-secondary);font-size:12px}.source-action-controls{display:flex;align-items:center;gap:8px;min-width:0}.source-connection{width:220px;min-width:0}.source-danger{margin-left:0}.empty{padding:28px;text-align:center;color:var(--td-text-color-placeholder)}
.field-help{color:var(--td-text-color-secondary);font-size:12px;line-height:1.5}.field-help code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
.folder-selection{display:flex;flex-direction:column;gap:7px;font-size:13px}.field-label{font-size:13px}.folder-selection-panel{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:14px;border:1px solid var(--td-component-border);border-radius:8px;background:var(--td-bg-color-secondarycontainer)}.folder-selection-panel p{margin:5px 0 0;color:var(--td-text-color-secondary);font-size:12px}.advanced-toggle{align-self:flex-start;padding:0;border:0;background:none;color:var(--td-brand-color);font:inherit;cursor:pointer}.manual-folder-url{display:flex;flex-direction:column;gap:7px}
.connection-row{display:grid;grid-template-columns:minmax(190px,1fr) 1fr 1fr auto auto auto;gap:10px;align-items:center;margin-top:14px;padding-top:14px;border-top:1px solid var(--td-component-stroke)}.connection-row p{margin:4px 0 0;color:var(--td-text-color-secondary);font-size:12px}.scan-interval{min-width:120px}.cadence-warning,.agent-upgrade-hint{color:var(--td-warning-color);font-size:12px;margin:0}.agent-upgrade-hint{white-space:nowrap}
@media(max-width:1000px){.source-actions{grid-template-columns:minmax(280px,1fr) minmax(340px,1fr)}.source-danger{grid-column:1/-1;justify-self:start}}@media(max-width:800px){.wedrive-page{padding:18px}.form-grid,.connection{grid-template-columns:1fr}.form-grid .wide{grid-column:auto}.agent-install,.folder-selection-panel{align-items:stretch;flex-direction:column}.source-actions{display:flex;align-items:stretch;flex-direction:column}.source-action-controls{width:100%;flex-wrap:wrap}.source-connection{width:100%}.source-danger{align-self:flex-start}}
</style>
