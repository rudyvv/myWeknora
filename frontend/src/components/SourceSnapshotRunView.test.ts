import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { compileScript, parse } from '@vue/compiler-sfc'
import { JSDOM } from 'jsdom'
import ts from 'typescript'
import zhCN from '../i18n/locales/zh-CN'

const dom = new JSDOM('<html><body></body></html>', { url: 'http://localhost/' })
for (const key of ['window', 'document', 'navigator', 'Element', 'HTMLElement', 'SVGElement', 'Node']) {
  Object.defineProperty(globalThis, key, { configurable: true, value: (dom.window as any)[key] })
}
const require = createRequire(import.meta.url)
const { createApp, h, nextTick, reactive } = require('vue') as typeof import('vue')
const { createI18n } = require('vue-i18n') as typeof import('vue-i18n')
const componentPath = fileURLToPath(new URL('./SourceSnapshotRunView.vue', import.meta.url))

function createTestApp(root: Parameters<typeof createApp>[0]) {
  const app = createApp(root)
  app.use(createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': zhCN } }))
  return app
}

function loadComponent(path: string, stubChildComponents = false): any {
  const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
  const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content,
    { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => {
    if (name.endsWith('.vue')) {
      return { __esModule: true, default: stubChildComponents ? {} : loadComponent(resolve(dirname(path), name), stubChildComponents) }
    }
    if (name === '@/api/wiki') return { readSourceWikiEvidence() { throw new Error('unexpected Wiki evidence reader') } }
    if (name === '@/api/knowledge-base') return { async getSourceFile() { return { data: { file_version_id: 'version-one', path: 'src/Service.java', content: 'class Service {}', commit_sha: 'a'.repeat(40), quality: 'structural', symbols: [] } } } }
    return require(name)
  }, module, module.exports)
  return module.exports.default
}

function loadSourceSnapshotRunView(stubChildComponents = false): any {
  return loadComponent(componentPath, stubChildComponents)
}

test('run details expose complete manifest and permit code reading only after publication', async () => {
  const result = reactive({ snapshot: { id: 'run-one', state: 'indexing', commit_sha: 'a'.repeat(40), project_id: '123', repository_url: 'https://gitlab.local/repo', manifest_complete: true, member_count: 2, file_count: 1, chunk_count: 2, previous_commit_sha: 'b'.repeat(40), parsed_count: 1, reused_file_count: 3, reused_chunk_count: 6, embedded_chunk_count: 2, reused_vector_count: 6, added_count: 1, changed_count: 2, deleted_count: 1, renamed_count: 1 },
    members: [{ path: 'src/Service.java', status: 'parsed', reason: '', source_file_id: 'file-one', file_version_id: 'version-one' }, { path: 'README.md', status: 'excluded', reason: 'outside selected paths', source_file_id: '', file_version_id: '' }] })
  const component = loadSourceSnapshotRunView()
  const host = document.createElement('div')
  document.body.append(host)
  const app = createTestApp({ render: () => h(component, { result, phase: 'running' }) })
  app.mount(host)
  try {
    await nextTick()
    assert.ok(host.textContent?.includes('正在建立索引'))
    assert.ok(host.textContent?.includes('运行阶段: 运行中'))
    assert.ok(host.textContent?.includes('成员清单完整'))
    assert.ok(host.textContent?.includes('已解析文件: 1'))
    assert.ok(host.textContent?.includes('复用文件: 3'))
    assert.ok(host.textContent?.includes('复用向量: 6'))
    assert.ok(host.textContent?.includes('已发布 SHA: ' + 'b'.repeat(40)))
    assert.ok(host.textContent?.includes('README.md'))
    assert.equal(Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes('src/Service.java')), undefined)
    result.snapshot.state = 'published'
    await nextTick()
    assert.ok(host.textContent?.includes('已发布 SHA: ' + 'a'.repeat(40)))
    const read = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('src/Service.java'))
    assert.ok(read)
    read.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('class Service {}'))
    assert.ok(host.textContent?.includes('原文只读'))
  } finally { app.unmount(); host.remove() }
})

test('failed source runs explain that the last complete publication remains active', async () => {
  const component = loadSourceSnapshotRunView(true)
  const host = document.createElement('div')
  document.body.append(host)
  const result = reactive({ snapshot: {
    id: 'failed-run', state: 'failed', commit_sha: '', detected_commit_sha: '', target_commit_sha: '', publication_checked: true,
    project_id: '123', repository_url: 'https://gitlab.local/repo', manifest_complete: false,
    member_count: 0, file_count: 0, chunk_count: 0, previous_commit_sha: 'b'.repeat(40),
    previous_published_at: '2026-09-29T01:02:03Z', error: 'GitLab branch is unavailable'
  }, members: [] })
  const app = createTestApp({ render: () => h(component, { result }) })
  app.mount(host)
  try {
    await nextTick()
    assert.ok(host.textContent?.includes('运行失败；已保留当前发布'))
    assert.ok(host.textContent?.includes('已发布 SHA: ' + 'b'.repeat(40)))
    assert.ok(host.textContent?.includes('最近成功发布: 2026-09-29T01:02:03Z'))
    assert.ok(host.textContent?.includes('检测到的 HEAD: 无法检测 HEAD'))
    assert.ok(host.textContent?.includes('处理目标: 尚未确定目标'))
    assert.ok(host.textContent?.includes('GitLab branch is unavailable'))
  } finally { app.unmount(); host.remove() }
})

test('first failed source publication does not claim that a version was retained', async () => {
  const component = loadSourceSnapshotRunView(true)
  const host = document.createElement('div')
  document.body.append(host)
  const result = reactive({ snapshot: {
    id: 'first-failed-run', state: 'failed', commit_sha: '', detected_commit_sha: '', target_commit_sha: '', publication_checked: true,
    project_id: '123', repository_url: 'https://gitlab.local/repo', manifest_complete: false,
    member_count: 0, file_count: 0, chunk_count: 0, error: 'branch does not exist'
  }, members: [] })
  const app = createTestApp({ render: () => h(component, { result }) })
  app.mount(host)
  try {
    await nextTick()
    assert.ok(host.textContent?.includes('首次发布失败；尚无已发布版本'))
    assert.ok(host.textContent?.includes('已发布 SHA: 尚未发布'))
    assert.ok(host.textContent?.includes('最近成功发布: 尚无成功发布'))
    assert.ok(host.textContent?.includes('branch does not exist'))
    assert.ok(!host.textContent?.includes('已保留当前发布'))
  } finally { app.unmount(); host.remove() }
})

test('failed source run does not claim first failure when publication state is unknown', async () => {
  const component = loadSourceSnapshotRunView(true)
  const host = document.createElement('div')
  document.body.append(host)
  const result = reactive({ snapshot: {
    id: 'unknown-publication-run', state: 'failed', commit_sha: '', detected_commit_sha: '', target_commit_sha: '',
    publication_checked: false, project_id: '123', repository_url: 'https://gitlab.local/repo', manifest_complete: false,
    member_count: 0, file_count: 0, chunk_count: 0, error: 'publication lookup failed'
  }, members: [] })
  const app = createTestApp({ render: () => h(component, { result }) })
  app.mount(host)
  try {
    await nextTick()
    assert.ok(host.textContent?.includes('运行失败；无法确认发布状态'))
    assert.ok(host.textContent?.includes('已发布 SHA: 无法确认发布状态'))
    assert.ok(host.textContent?.includes('最近成功发布: 无法确认发布状态'))
    assert.ok(host.textContent?.includes('publication lookup failed'))
    assert.ok(!host.textContent?.includes('首次发布失败'))
  } finally { app.unmount(); host.remove() }
})

test('failed target is not reported as published and absent telemetry stays unknown', async () => {
  const component = loadSourceSnapshotRunView(true)
  const host = document.createElement('div')
  document.body.append(host)
  const target = 'd'.repeat(40)
  const result = reactive({ snapshot: {
    id: 'failed-target', state: 'failed', commit_sha: target, detected_commit_sha: 'e'.repeat(40), target_commit_sha: target,
    publication_checked: true, project_id: '123', repository_url: 'https://gitlab.local/repo', manifest_complete: false,
  }, members: [] })
  const app = createTestApp({ render: () => h(component, { result, phase: 'failed' }) })
  app.mount(host)
  try {
    await nextTick()
    assert.ok(host.textContent?.includes(`处理目标: ${target}`))
    assert.ok(host.textContent?.includes('已发布 SHA: 尚未发布'))
    assert.ok(!host.textContent?.includes(`已发布 SHA: ${target}`))
    assert.ok(host.textContent?.includes('运行阶段: 失败'))
    assert.ok(host.textContent?.includes('纳入文件: 未知'))
    assert.ok(host.textContent?.includes('估算输入 Token: 未知'))
    assert.ok(host.textContent?.includes('缓存: 未知'))
    assert.ok(host.textContent?.includes('符合条件: 未知'))
  } finally { app.unmount(); host.remove() }
})

test('published snapshot proves publication but does not invent a missing SHA', async () => {
  const component = loadSourceSnapshotRunView(true)
  const host = document.createElement('div')
  document.body.append(host)
  const result = reactive({ snapshot: {
    id: 'published-without-sha', state: 'published', commit_sha: '', project_id: '123', repository_url: 'https://gitlab.local/repo',
    publication_checked: true, published_at: '2026-10-01T12:00:00Z',
  }, members: [] })
  const app = createTestApp({ render: () => h(component, { result }) })
  app.mount(host)
  try {
    await nextTick()
    assert.ok(host.textContent?.includes('已发布 SHA: 已发布（SHA 不可用）'))
    assert.ok(host.textContent?.includes('最近成功发布: 2026-10-01T12:00:00Z'))
    assert.ok(!host.textContent?.includes('已发布 SHA: 尚未发布'))
  } finally { app.unmount(); host.remove() }
})

test('source run view renders only allowlisted measured telemetry and preserves observed zero', async () => {
  const component = loadSourceSnapshotRunView(true)
  const host = document.createElement('div')
  document.body.append(host)
  const result = reactive({
    snapshot: {
      id: 'measured-run', state: 'published', commit_sha: 'a'.repeat(40), detected_commit_sha: 'b'.repeat(40),
      target_commit_sha: 'a'.repeat(40), publication_checked: true, project_id: '123', repository_url: 'https://gitlab.local/repo',
      manifest_complete: true, member_count: 0, file_count: 1, chunk_count: 2,
    },
    members: [],
    telemetry: {
      schema_version: 1 as const,
      phase_duration_ms: { fetching: 1234, parsing: 0, indexing: Number.NaN, publishing: 4321, ready: 999 },
      selected_bytes: 0,
      storage: {
        cache: { used_bytes: 0, limit_bytes: 0, measurement: 'logical_payload' as const },
        vectors: { used_bytes: 1024, measurement: 'physical' as const },
      },
      quality_counts: { structural: 1, syntax_error: 0, partial: -1, invented_metric: 99 },
      model_usage: { embedding_calls: 0, generation_calls: 2, input_tokens: 5, output_tokens: 3, estimated_input_tokens: 40 },
      lease_recoveries: 0,
      cleanup_residue_count: 0,
      published_commit_sha: 'a'.repeat(40),
      wiki_coverage: { eligible: 1, ready: 0, stale: 0, failed: 0, ungenerated: 1, deferred: 0 },
    },
  })
  const app = createTestApp({ render: () => h(component, { result, phase: 'published' }) })
  app.mount(host)
  try {
    await nextTick()
    const text = host.textContent || ''
    assert.ok(text.includes('纳入原始字节数: 0 B'))
    assert.ok(text.includes('获取中: 1234 毫秒'))
    assert.ok(text.includes('解析中: 0 毫秒'))
    assert.ok(text.includes('建立索引中: 未知'))
    assert.ok(text.includes('结构化: 1'))
    assert.ok(text.includes('语法错误: 0'))
    assert.ok(!text.includes('invented_metric'))
    assert.ok(text.includes('缓存: 已用 0 B · 限额 0 B · 逻辑载荷'))
    assert.ok(text.includes('保留原文: 未知'))
    assert.ok(text.includes('向量: 已用 1 KiB · 限额未报告 · 物理测量'))
    assert.ok(text.includes('实际输入 Token: 5'))
    assert.ok(text.includes('估算输入 Token: 40'))
    assert.ok(text.includes('Embedding 调用: 0'))
    assert.ok(text.includes('符合条件: 1'))
    assert.ok(text.includes('就绪: 0'))
    assert.ok(text.includes('租约恢复次数: 0 · 残留清理项: 0'))
    assert.ok(text.includes('已发布 SHA: ' + 'a'.repeat(40)))
  } finally { app.unmount(); host.remove() }
})
