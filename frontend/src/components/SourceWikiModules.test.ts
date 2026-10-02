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
