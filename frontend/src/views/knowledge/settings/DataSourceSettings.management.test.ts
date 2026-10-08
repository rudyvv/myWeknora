import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'

const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/', pretendToBeVisual: true })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'MutationObserver', 'Event', 'MouseEvent', 'KeyboardEvent', 'getComputedStyle', 'localStorage', 'requestAnimationFrame', 'cancelAnimationFrame']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}

const require = createRequire(import.meta.url)
const { createApp, defineComponent, h, nextTick } = require('vue') as any
const settingsPath = fileURLToPath(new URL('./DataSourceSettings.vue', import.meta.url))
const sourceRoot = resolve(dirname(settingsPath), '../../..')
const apiPath = resolve(sourceRoot, 'api/datasource/index.ts')

async function settle() {
  for (let i = 0; i < 5; i++) {
    await nextTick()
    await new Promise<void>(resolve => setImmediate(resolve))
  }
}

function copy<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T
}

function source(id: string, contentMode: 'source' | 'document' = 'source', sourceLifecycle?: any) {
  return {
    id,
    tenant_id: 1,
    knowledge_base_id: 'kb-one',
    name: id,
    type: 'gitlab',
    config: { settings: { content_mode: contentMode } },
    sync_schedule: '0 0 */6 * * *',
    sync_mode: 'incremental',
    status: 'active',
    conflict_strategy: 'overwrite',
    sync_deletions: true,
    last_sync_at: null,
    last_sync_result: null,
    error_message: '',
    created_at: '',
    updated_at: '',
    source_lifecycle: sourceLifecycle ?? (contentMode === 'source'
      ? { binding_state: 'bound', query_enabled: true }
      : undefined),
  }
}

async function fixture(options: { admin?: boolean; sources: any[] }) {
  const calls: Array<{ method: string; args: any[] }> = []
  let rows = copy(options.sources)
  let holdNextList = false
  let releaseHeldList: (() => void) | undefined
  let failNextList = false
  let holdNextDelete = false
  let releaseHeldDelete: (() => void) | undefined
  const record = (method: string, ...args: any[]) => calls.push({ method, args: copy(args) })
  const api = {
    async listDataSources() {
      if (failNextList) {
        failNextList = false
        throw new Error('fixture list failure')
      }
      const reply = { data: copy(rows) }
      if (holdNextList) {
        holdNextList = false
        return new Promise(resolve => { releaseHeldList = () => resolve(reply) })
      }
      return reply
    },
    async deleteDataSource(id: string) {
      record('deleteDataSource', id)
      rows = rows.filter(row => row.id !== id)
      if (holdNextDelete) {
        holdNextDelete = false
        await new Promise<void>(resolve => { releaseHeldDelete = resolve })
      }
    },
    async triggerSync(id: string) { record('triggerSync', id) },
    async pauseDataSource(id: string) { record('pauseDataSource', id) },
    async resumeDataSource(id: string) { record('resumeDataSource', id) },

  }

  const componentStub = defineComponent({ setup: () => () => null })
  const modules = new Map<string, any>()
  function loadComponent(path: string): any {
    if (modules.has(path)) return modules.get(path)
    const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
    const script = compileScript(descriptor, { id: path, inlineTemplate: true }).content
    const compiled = ts.transpileModule(script, {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    }).outputText
    const module = { exports: {} as any }
    const load = (name: string): any => {
      if (name === '@/api/datasource') return api
      if (name === 'vue-i18n') return { useI18n: () => ({ t: (key: string) => key }) }
      if (name === 'vue-router') return { useRouter: () => ({ push: () => undefined }) }
      if (name === '@/stores/auth') return { useAuthStore: () => ({ hasRole: (role: string) => role === 'admin' && options.admin !== false }) }
      if (name === '@/utils/cronHumanize') return { humanizeCron: (value: string) => value, relativeTime: () => '' }
      if (name === './syncLogDisplay') return { syncLogDisplayStatus: (log: any) => log.status }
      if (name === 'tdesign-vue-next') return { MessagePlugin: { success: () => undefined, error: () => undefined } }
      if (name.endsWith('.vue')) return { __esModule: true, default: componentStub }
      return require(name)
    }
    new Function('require', 'module', 'exports', compiled)(load, module, module.exports)
    modules.set(path, module.exports.default)
    return module.exports.default
  }

  const PopconfirmStub = defineComponent({
    props: ['content'],
    emits: ['confirm'],
    setup(props: any, { emit, slots }: any) {
      return () => h('div', { class: 'test-popconfirm', 'data-confirm-content': props.content }, [
        ...(slots.default?.() ?? []),
        h('button', { class: 'test-confirm', type: 'button', onClick: () => emit('confirm') }, 'Confirm'),
      ])
    },
  })
  const dropdownStub = defineComponent({
    inheritAttrs: false,
    setup(_props: any, { attrs, slots }: any) {
      return () => h('t-dropdown', attrs, [...(slots.default?.() ?? []), ...(slots.dropdown?.() ?? [])])
    },
  })
  const passthroughStub = (tag: string) => defineComponent({
    inheritAttrs: false,
    setup(_props: any, { attrs, slots }: any) {
      return () => h(tag, attrs, slots.default?.())
    },
  })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(loadComponent(settingsPath), { kbId: 'kb-one' }) })
  app.component('t-dropdown', dropdownStub)
  app.component('t-popconfirm', PopconfirmStub)
  for (const tag of ['t-dropdown-item', 't-dropdown-menu', 't-loading', 't-empty', 't-icon', 't-button', 't-tooltip']) {
    app.component(tag, passthroughStub(tag))
  }
  app.mount(host)
  await settle()

  function menuItems() {
    return Array.from(host.querySelectorAll('t-dropdown-item')) as HTMLElement[]
  }
  function menuItemText(item: HTMLElement) {
    return item.querySelector('.ds-dropdown-delete-trigger')?.textContent?.trim() ?? item.textContent?.trim()
  }
  function hasMenuItem(label: string) {
    return menuItems().some(item => menuItemText(item) === label)
  }
  async function clickMenuItem(label: string) {
    const item = menuItems().find(entry => menuItemText(entry) === label)
    assert.ok(item, `Visible datasource action: ${label}`)
    item.dispatchEvent(new dom.window.MouseEvent('click', { bubbles: true }))
    await settle()
  }
  async function confirm(content: string) {
    const popconfirm = host.querySelector(`[data-confirm-content="${content}"] .test-confirm`) as HTMLButtonElement | null
    assert.ok(popconfirm, `Confirmation prompt: ${content}`)
    popconfirm.click()
    await settle()
  }
  return {
    calls,
    host,
    holdNextList() { holdNextList = true },
    failNextList() { failNextList = true },
    async releaseHeldList() { releaseHeldList?.(); await settle() },
    holdNextDelete() { holdNextDelete = true },
    async releaseHeldDelete() { releaseHeldDelete?.(); await settle() },
    hasMenuItem,
    clickMenuItem,
    confirm,
    async close() {
      app.unmount()
      await settle()
      document.body.innerHTML = ''
    },
  }
}

test('source uses the same DELETE endpoint and removes its card without clearing knowledge', async () => {
  const f = await fixture({ sources: [source('source-one')] })
  try {
    assert.ok(f.hasMenuItem('datasource.delete'))
    assert.ok(!f.hasMenuItem('datasource.unbind'))
    assert.ok(!f.hasMenuItem('datasource.sourceClear'))
    assert.ok(!f.hasMenuItem('datasource.sourceClearRetry'))
    await f.clickMenuItem('datasource.delete')
    await f.confirm('datasource.deleteConfirm')
    assert.deepEqual(f.calls.filter(call => call.method === 'deleteDataSource').map(call => call.args), [['source-one']])
    assert.ok(!f.host.textContent?.includes('source-one'))
  } finally { await f.close() }
})

test('previously unbound sources can be removed through the unified delete action', async () => {
  const f = await fixture({ sources: [source('source-one', 'source', { binding_state: 'unbound', query_enabled: true })] })
  try {
    assert.ok(f.hasMenuItem('datasource.delete'))
    assert.ok(!f.hasMenuItem('datasource.syncNow'))
    await f.clickMenuItem('datasource.delete')
    await f.confirm('datasource.deleteConfirm')
    assert.ok(!f.host.textContent?.includes('source-one'))
  } finally { await f.close() }
})

test('pausing a bound source leaves its query-visibility state untouched', async () => {
  const f = await fixture({ sources: [source('source-one')] })
  try {
    await f.clickMenuItem('datasource.pause')
    assert.ok(f.calls.some(call => call.method === 'pauseDataSource'))
    assert.ok(f.host.textContent?.includes('datasource.sourceLifecycle.queryEnabled'))
    assert.ok(!f.calls.some(call => call.method === 'unbindDataSource' || call.method === 'clearSourceKnowledge'))
  } finally { await f.close() }
})

test('legacy cleanup state exposes no clear or retry operation', async () => {
  const f = await fixture({ sources: [source('source-one', 'source', {
    binding_state: 'unbound', query_enabled: false,
    cleanup: { id: 'cleanup-retry', status: 'failed', retryable: true },
  })] })
  try {
    assert.ok(!f.hasMenuItem('datasource.sourceClearRetry'))
    assert.ok(!f.hasMenuItem('datasource.sourceClear'))
    assert.ok(f.hasMenuItem('datasource.delete'))
    assert.ok(!f.hasMenuItem('datasource.syncNow'))
  } finally { await f.close() }
})

test('viewer cannot mutate sources and document-mode datasource keeps its legacy delete behavior', async () => {
  const viewer = await fixture({ admin: false, sources: [source('source-one')] })
  try {
    assert.ok(!viewer.hasMenuItem('datasource.edit'))
    assert.ok(!viewer.hasMenuItem('datasource.syncNow'))
    assert.ok(!viewer.hasMenuItem('datasource.delete'))
    assert.ok(!viewer.hasMenuItem('datasource.unbind'))
    assert.ok(!viewer.hasMenuItem('datasource.sourceClear'))
    assert.ok(viewer.hasMenuItem('datasource.logs'))
  } finally { await viewer.close() }

  const doc = await fixture({ sources: [source('document-one', 'document')] })
  try {
    assert.ok(doc.hasMenuItem('datasource.delete'))
    assert.ok(doc.hasMenuItem('datasource.pause'))
    assert.ok(!doc.hasMenuItem('datasource.unbind'))
    assert.ok(!doc.hasMenuItem('datasource.sourceClear'))
    await doc.clickMenuItem('datasource.delete')
    await doc.confirm('datasource.deleteConfirm')
    assert.ok(doc.calls.some(call => call.method === 'deleteDataSource'))
    assert.ok(!doc.calls.some(call => call.method === 'clearSourceKnowledge' || call.method === 'unbindDataSource'))
  } finally { await doc.close() }
})

test('late list cannot restore a deleted source when refresh fails', async () => {
  const f = await fixture({ sources: [source('source-one')] })
  try {
    f.holdNextList()
    await f.clickMenuItem('datasource.syncNow')
    f.failNextList()
    await f.clickMenuItem('datasource.delete')
    await f.confirm('datasource.deleteConfirm')
    assert.ok(!f.host.textContent?.includes('source-one'))
    await f.releaseHeldList()
    assert.ok(!f.host.textContent?.includes('source-one'))
  } finally { await f.releaseHeldList(); await f.close() }
})

test('source deletion prevents duplicate deletion and concurrent connection actions', async () => {
  const f = await fixture({ sources: [source('source-one')] })
  try {
    f.holdNextDelete()
    await f.clickMenuItem('datasource.delete')
    await f.confirm('datasource.deleteConfirm')
    assert.equal(f.calls.filter(call => call.method === 'deleteDataSource').length, 1)
    assert.ok(!f.hasMenuItem('datasource.delete'))
    assert.ok(!f.hasMenuItem('datasource.syncNow'))
    assert.ok(!f.hasMenuItem('datasource.edit'))
    await f.releaseHeldDelete()
    assert.ok(!f.host.textContent?.includes('source-one'))
  } finally { await f.releaseHeldDelete(); await f.close() }
})
