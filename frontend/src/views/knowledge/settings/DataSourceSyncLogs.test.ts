import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'

const dom = new JSDOM('<html><body></body></html>', { url: 'http://localhost/' })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}
const require = createRequire(import.meta.url)
const { createApp, h, nextTick, reactive } = require('vue') as typeof import('vue')

async function settle() {
  for (let i = 0; i < 5; i++) {
    await nextTick()
    await new Promise<void>(resolve => setImmediate(resolve))
  }
}

function loadSyncLogs(apis: Record<string, any>) {
  const path = fileURLToPath(new URL('./DataSourceSyncLogs.vue', import.meta.url))
  const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
  const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => apis[name] || require(name), module, module.exports)
  return module.exports.default
}

function loadSyncLogDisplay() {
  const path = fileURLToPath(new URL('./syncLogDisplay.ts', import.meta.url))
  const code = ts.transpileModule(readFileSync(path, 'utf8'), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', code)(require, module, module.exports)
  return module.exports
}

test('sync history distinguishes initial queue, catch-up, retry wait and hard failure', async () => {
  const log = (id: string, status: string, source_run_phase: string) => ({
    id, data_source_id: 'source-1', status, source_run_phase,
    started_at: '2026-09-30T01:02:03Z', finished_at: null,
    items_total: 0, items_created: 0, items_updated: 0, items_deleted: 0,
    items_skipped: 0, items_failed: 0, error_message: '',
  })
  const logs = [
    log('first', 'queued', 'queued'),
    log('catch-up', 'queued', 'waiting_for_catch_up'),
    log('retry', 'queued', 'retry_wait'),
    log('failed', 'failed', 'failed'),
  ]
  const labels: Record<string, string> = {
    'datasource.logStatus.queued': '待同步',
    'datasource.logStatus.waiting_for_catch_up': '等待追赶',
    'datasource.logStatus.retry_wait': '等待重试',
    'datasource.logStatus.failed': '失败',
  }
  const view = loadSyncLogs({
    '@/api/datasource': { async getSyncLogs() { return { data: logs } } },
    'vue-i18n': { useI18n: () => ({ t: (key: string) => labels[key] || key }) },
    '@/components/SourceSnapshotRunView.vue': { __esModule: true, default: { render: () => null } },
    './syncLogDisplay': loadSyncLogDisplay(),
  })
  const props = reactive({ dataSourceId: 'source-1', dataSourceName: 'Repository', dataSourceType: 'gitlab', visible: false })
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(view, props) })
  app.mount(host)
  try {
    props.visible = true
    await settle()
    const statuses = Array.from(host.querySelectorAll<HTMLElement>('.tl-status')).map(node => node.textContent?.trim())
    assert.deepEqual(statuses, ['待同步', '等待追赶', '等待重试', '失败'])
  } finally {
    app.unmount()
    host.remove()
  }
})
