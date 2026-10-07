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
async function settle() { for (let i = 0; i < 5; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) } }
function component(name: string, apis: Record<string, any>) {
 const path = fileURLToPath(new URL(name, import.meta.url))
 const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
 const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
 const module = { exports: {} as any }
 new Function('require', 'module', 'exports', compiled)((name: string) => apis[name] || require(name), module, module.exports)
 return module.exports.default
}
test('source repository selector accepts the datasource API array and submits module generation', async () => {
 const requests: any[] = []
 const view = component('./SourceWikiModules.vue', {
  '@/api/datasource': { async listDataSources() { return [
   { id: 'source-repo', name: 'Source repository', config: { settings: { content_mode: 'source' } } },
   { id: 'document-repo', name: 'Document repository', config: { settings: { content_mode: 'document' } } }
  ] } },
  '@/api/wiki': {
   async listSourceWikiAttempts() { return { data: [] } },
   async listSourceWikiCoverage() { return { data: [] } },
   async listSourceWikiBatches() { return { data: [] } },
   async generateSourceWikiModule(_kb: string, request: any) { requests.push(request); return { data: { status: 'ready', slug: 'module-new' } } }
  }
 })
 const host = document.createElement('div'); document.body.append(host)
 const app = createApp({ render: () => h(view, { kbId: 'kb-one', canEdit: true }) }); app.mount(host)
 try {
  await settle()
  const select = host.querySelector<HTMLSelectElement>('[aria-label="技术卡片仓库"]')
  assert.ok(select, 'the generation form must remain available with a datasource API array')
  assert.deepEqual(Array.from(select.options).map(option => option.value), ['source-repo'])
  for (const [selector, value] of [['[aria-label="模块目录"]', 'src/module'], ['[aria-label="技术卡片主题"]', 'Module responsibilities']]) {
   const input = host.querySelector<HTMLInputElement>(selector)!; input.value = value; input.dispatchEvent(new window.Event('input', { bubbles: true }))
  }
  await settle()
  const generate = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === '生成模块卡片')!
  assert.equal(generate.disabled, false)
  host.querySelector('form')!.dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true })); await settle()
  assert.deepEqual(requests, [{ source_id: 'source-repo', module_path: 'src/module', title: 'Module responsibilities' }])
 } finally { app.unmount(); host.remove() }
})
test('module failure reason is visible and manual retry uses a fresh bounded attempt', async () => {
 let attempts = [{ id: 'failed-one', source_id: 'repo-one', module_path: 'src/scheduling', title: 'Scheduling', slug: 'module-one', status: 'failed', reason: 'fabricated evidence e999', calls: 3, tokens: 8000, repairs: 2 }]
 const requests: any[] = [], ready: string[] = []
 const view = component('./SourceWikiModules.vue', {
  '@/api/datasource': { async listDataSources() { return { data: [{ id: 'repo-one', name: 'Repository', config: { settings: { content_mode: 'source' } } }] } } },
  '@/api/wiki': {
   async listSourceWikiAttempts() { return { data: attempts } },
   async listSourceWikiCoverage() { return { data: [] } },
   async generateSourceWikiModule(_kb: string, request: any) { requests.push(request); attempts = [{ ...attempts[0], id: 'retry-two', status: 'ready', reason: '', calls: 2, repairs: 0 }]; return { data: attempts[0] } }
  }
 })
 const host = document.createElement('div'); document.body.append(host)
 const app = createApp({ render: () => h(view, { kbId: 'kb-one', canEdit: true, onReady: (slug: string) => ready.push(slug) }) })
 app.mount(host)
 try {
  await settle()
  assert.ok(host.textContent?.includes('fabricated evidence e999'))
  assert.ok(host.textContent?.includes('修复 2/2'))
  const retry = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent === '手动重试')
  assert.ok(retry); retry.click(); await settle()
  assert.deepEqual(requests, [{ source_id: 'repo-one', module_path: 'src/scheduling', title: 'Scheduling' }])
  assert.deepEqual(ready, ['module-one'])
  assert.ok(host.textContent?.includes('已就绪'))
 } finally { app.unmount(); host.remove() }
})
test('Wiki body owner opens exact evidence in the source viewer and rejects SHA substitution', async () => {
 const requests: any[] = []
 const view = component('./SourceCodeView.vue', {
  '@/utils/sourceQuality': require('../utils/sourceQuality.ts'),
  '@/utils/referenceSources': require('../utils/referenceSources.ts'),
  '@/components/SourceRegionBadge.vue': { render: () => null },
  '@/api/knowledge-base': { async getSourceFile() { throw new Error('unexpected ordinary source read') } },
  '@/api/wiki': { async readSourceWikiEvidence(...args: any[]) { requests.push(args); return { data: { knowledge_id: 'file-one', file_version_id: 'version-one', commit_sha: 'a'.repeat(40), path: 'src/Service.java', repository_url: 'https://gitlab.local/team/repo', content: 'class Service {\r\n String getPushSchedule() { return "scheduled"; }\r\n}', symbols: [], encoding: 'utf-8', quality: 'structural' } } } }
 })
 const host = document.createElement('div'); document.body.append(host)
 const props = reactive({ knowledgeId: 'file-one', fileVersionId: 'version-one', wikiEvidence: { kbId: 'kb-one', slug: 'module-one', id: 'e001', version: 1, commitSHA: 'a'.repeat(40) }, evidenceRange: { start_line: 2, end_line: 2, start_byte: 17, end_byte: 80 } })
 const app = createApp({ render: () => h(view, props) }); app.mount(host)
 try {
  await settle()
  assert.deepEqual(requests, [['kb-one', 'module-one', 'e001', 1]])
  assert.ok(host.textContent?.includes('getPushSchedule'))
  assert.ok(host.querySelector('[data-line="2"]')?.classList.contains('selected'))
  props.wikiEvidence = { ...props.wikiEvidence, commitSHA: 'b'.repeat(40) }; await settle()
  assert.ok(host.querySelector('[role="alert"]'))
  assert.ok(!host.textContent?.includes('getPushSchedule'))
 } finally { app.unmount(); host.remove() }
})

test('switching KB discards a pending generation result and reason', async () => {
 for (const status of ['ready', 'failed']) {
  let resolve!: (value: any) => void
  const pending = new Promise(resolveResult => { resolve = resolveResult })
  const ready: string[] = []
  const view = component('./SourceWikiModules.vue', {
   '@/api/datasource': { async listDataSources(kb: string) { return { data: [{ id: kb+'-source', name: kb, config: { settings: { content_mode: 'source' } } }] } } },
   '@/api/wiki': { async listSourceWikiAttempts() { return { data: [] } }, async listSourceWikiCoverage() { return { data: [] } }, async generateSourceWikiModule() { return pending } }
  })
  const props = reactive({ kbId: 'old-kb', canEdit: true })
  const host = document.createElement('div'); document.body.append(host)
  const app = createApp({ render: () => h(view, { ...props, onReady: (slug: string) => ready.push(slug) }) }); app.mount(host)
  try {
   await settle()
   for (const [selector, value] of [['[aria-label="模块目录"]', 'src'], ['[aria-label="技术卡片主题"]', 'Schedule']]) {
    const input = host.querySelector(selector) as HTMLInputElement; input.value = value; input.dispatchEvent(new window.Event('input', { bubbles: true }))
   }
   await settle(); host.querySelector('form')!.dispatchEvent(new window.Event('submit', { bubbles: true, cancelable: true })); await settle()
   props.kbId = 'new-kb'; await settle()
   resolve({ data: { status, slug: 'old-slug', reason: 'old-kb private failure' } }); await settle()
   assert.deepEqual(ready, [])
   assert.ok(!host.textContent?.includes('old-kb private failure'))
   assert.ok(!host.textContent?.includes('old-kb'))
  } finally { app.unmount(); host.remove() }
 }
})

test('coverage distinguishes backlog and uncertainty while ready topics keep their existing Wiki link', async () => {
 const coverage = [
  { source_id: 'repo-one', topic_key: 'system', snapshot_id: 'snapshot-one', kind: 'system', title: 'System overview', priority: 120, status: 'planned', uncertain: false, uncertainty_reasons: [] },
  { source_id: 'repo-one', topic_key: 'flow/POST /orders', snapshot_id: 'snapshot-one', kind: 'flow', title: 'POST /orders', priority: 100, status: 'expansion', uncertain: true, uncertainty_reasons: ['No static backend route was found'] },
  { source_id: 'repo-one', topic_key: 'module/src/orders', snapshot_id: 'snapshot-one', kind: 'module', module_path: 'src/orders', title: 'Orders', priority: 90, status: 'ready', uncertain: false, uncertainty_reasons: [], wiki_slug: 'concept/source-repo-one/module-orders' },
 ]
 const ready: string[] = []
 const view = component('./SourceWikiModules.vue', {
  '@/api/datasource': { async listDataSources() { return { data: [{ id: 'repo-one', name: 'Repository', config: { settings: { content_mode: 'source' } } }] } } },
  '@/api/wiki': { async listSourceWikiAttempts() { return { data: [] } }, async listSourceWikiCoverage(_kb: string, source: string) { assert.equal(source, 'repo-one'); return { data: coverage } } }
 })
 const host = document.createElement('div'); document.body.append(host)
 const app = createApp({ render: () => h(view, { kbId: 'kb-one', canEdit: false, onReady: (slug: string) => ready.push(slug) }) })
 app.mount(host)
 try {
  await settle()
  assert.ok(host.textContent?.includes('计划中'))
  assert.ok(host.textContent?.includes('待扩展'))
  assert.ok(host.textContent?.includes('关系不确定'))
  assert.ok(host.textContent?.includes('No static backend route was found'))
  const read = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === '阅读卡片')
  assert.ok(read); read.click(); await settle()
  assert.deepEqual(ready, ['concept/source-repo-one/module-orders'])
 } finally { app.unmount(); host.remove() }
})

test('failed system and flow attempts retry only through bounded batch preflight and start', async () => {
 const attempts = [
  { id: 'system-failed', source_id: 'repo-one', module_path: '', title: 'System overview', slug: '', status: 'failed', reason: 'batch stopped', calls: 1, tokens: 1000, repairs: 0 },
  { id: 'flow-failed', source_id: 'repo-one', module_path: '', title: 'POST /orders', slug: '', status: 'failed', reason: 'batch stopped', calls: 1, tokens: 1000, repairs: 0 },
  { id: 'system-staged', source_id: 'repo-one', module_path: '', title: 'Pending overview', slug: '', status: 'staged', reason: '', calls: 0, tokens: 0, repairs: 0 },
 ]
 const preflightSources: string[] = [], startedSources: string[] = [], moduleRequests: any[] = []
 let batchStatus = 'failed'
 const view = component('./SourceWikiModules.vue', {
  '@/api/datasource': { async listDataSources() { return { data: [{ id: 'repo-one', name: 'Repository', config: { settings: { content_mode: 'source' } } }] } } },
  '@/api/wiki': {
   async listSourceWikiAttempts() { return { data: attempts } },
   async listSourceWikiCoverage() { return { data: [] } },
   async listSourceWikiBatches(_kb: string, source: string) { return { data: [{ id: 'batch-one', source_id: source, status: batchStatus, phase: 'qa', cursor: 2, publish_cursor: 0, initial_count: 2, calls_reserved: 2, tokens_reserved: 2000 }] } },
   async preflightSourceWikiBatch(_kb: string, source: string) {
    preflightSources.push(source)
    return { data: { preflight_passed: true, start_available: true, source_id: source, snapshot_id: 'snapshot-one', commit_sha: 'a'.repeat(40), source_updated_at: '2026-10-02T00:00:00Z', model_id: 'model-one', model_updated_at: '2026-10-02T00:00:00Z', model_context_window: 65536, model_context_known: true, max_completion_tokens: 4096, candidate_count: 3, initial_count: 2, expansion_count: 1, module_count: 0, flow_count: 1, initial_topics: [], max_calls: 18, max_tokens: 360000, max_elapsed_ms: 180000, max_initial_topics: 64, skeleton_max_calls: 8, skeleton_max_tokens: 100000, qa_max_calls: 10, qa_max_tokens: 260000 } }
   },
   async startSourceWikiBatch(_kb: string, source: string) { startedSources.push(source); batchStatus = 'queued'; return { data: { id: 'batch-two', source_id: source, status: 'queued', phase: 'skeleton', cursor: 0, publish_cursor: 0, initial_count: 2, calls_reserved: 0, tokens_reserved: 0 } } },
   async generateSourceWikiModule(_kb: string, request: any) { moduleRequests.push(request); return { data: { status: 'ready', slug: 'unexpected' } } },
  }
 })
 const host = document.createElement('div'); document.body.append(host)
 const app = createApp({ render: () => h(view, { kbId: 'kb-one', canEdit: true }) })
 app.mount(host)
 try {
  await settle()
  const systemRow = Array.from(host.querySelectorAll('li')).find(row => row.querySelector('strong')?.textContent === 'System overview')
  const flowRow = Array.from(host.querySelectorAll('li')).find(row => row.querySelector('strong')?.textContent === 'POST /orders')
  const stagedRow = Array.from(host.querySelectorAll('li')).find(row => row.querySelector('strong')?.textContent === 'Pending overview')
  assert.ok(systemRow); assert.ok(flowRow); assert.ok(stagedRow)
  assert.ok(systemRow.textContent?.includes('批次失败'))
  assert.ok(flowRow.textContent?.includes('批次失败'))
  assert.ok(stagedRow.textContent?.includes('待整批QA'))
  assert.ok(!stagedRow.textContent?.includes('失败，草稿已保留'))
  for (const row of [systemRow, flowRow]) {
   const retry = Array.from(row.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === '失败批次重新预检')
   assert.ok(retry); retry.click(); await settle()
  }
  assert.deepEqual(preflightSources, ['repo-one', 'repo-one'])
  assert.deepEqual(moduleRequests, [], 'batch system/flow attempts must never call the module-generation API with an empty path')
  const start = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === '启动这批 2 张卡片')
  assert.ok(start); start.click(); await settle()
  assert.deepEqual(startedSources, ['repo-one'], 'retry follows the existing preflight then bounded batch-start path')
  for (const row of [systemRow, flowRow]) {
   const retry = Array.from(row.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === '失败批次重新预检')
   assert.ok(retry); assert.ok(retry.disabled, 'a queued/running parent batch blocks another retry')
  }
  assert.deepEqual(moduleRequests, [])
 } finally { app.unmount(); host.remove() }
})

test('ordinary Wiki revision selection shows its body without a selection hint', async () => {
 const revision = { id: 'revision-one', version: 1, title: 'Original title', content: 'Original document body', summary: '', edit_source: 'human', edited_at: '2026-09-29T00:00:00Z' }
 const view = component('../views/knowledge/wiki/WikiRevisionDrawer.vue', {
  'vue-i18n': { useI18n: () => ({ t: (key: string) => key }) },
  'tdesign-vue-next': { MessagePlugin: { error: (error: string) => { throw new Error(error) } } },
  '@/components/SourceCodeView.vue': { render: () => null },
  '@/components/settings/SettingDrawer.vue': { setup(_: unknown, { slots }: any) { return () => h('div', slots.default?.()) } },
  '@/api/wiki': { async listWikiRevisions() { return { data: { revisions: [revision], total: 1 } } }, async getWikiRevision() { return { data: revision } } },
  '@/utils/wikiRevisionDiff': require('../utils/wikiRevisionDiff.ts'),
 })
 const props = reactive({ visible: false, kbId: 'kb-one', slug: 'document', currentPage: { version: 2, title: 'Current title', content: 'Current document body', summary: '' }, canEdit: false })
 const host = document.createElement('div'); document.body.append(host)
 const app = createApp({ render: () => h(view, props) }); app.mount(host)
 try {
  props.visible = true; await settle()
  const historical = Array.from(host.querySelectorAll<HTMLElement>('.wiki-rev-item')).find(item => item.querySelector('.wiki-rev-version')?.textContent === 'v1')
  assert.ok(historical); historical.click(); await settle()
  const raw = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(button => button.textContent === 'knowledgeEditor.wikiBrowser.revisionRaw')
  assert.ok(raw); raw.click(); await settle()
  assert.ok(host.textContent?.includes('Original document body'))
  assert.equal(host.querySelector('.wiki-rev-detail-hint'), null)
 } finally { app.unmount(); host.remove() }
})
