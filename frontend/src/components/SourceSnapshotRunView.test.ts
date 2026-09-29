import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, resolve } from 'node:path'
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

test('run details expose complete manifest and permit code reading only after publication', async () => {
  const componentPath = fileURLToPath(new URL('./SourceSnapshotRunView.vue', import.meta.url))
  function load(path: string): any {
    const { descriptor } = parse(readFileSync(path, 'utf8'), { filename: path })
    const compiled = ts.transpileModule(compileScript(descriptor, { id: path, inlineTemplate: true }).content,
      { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
    const module = { exports: {} as any }
    new Function('require', 'module', 'exports', compiled)((name: string) => {
      if (name.endsWith('.vue')) return { __esModule: true, default: load(resolve(dirname(path), name)) }
      if (name === '@/api/wiki') return { readSourceWikiEvidence() { throw new Error('unexpected Wiki evidence reader') } }
      if (name === '@/api/knowledge-base') return { async getSourceFile() { return { data: { file_version_id: 'version-one', path: 'src/Service.java', content: 'class Service {}', commit_sha: 'a'.repeat(40), quality: 'structural', symbols: [] } } } }
      return require(name)
    }, module, module.exports)
    return module.exports.default
  }
  const result = reactive({ snapshot: { id: 'run-one', state: 'indexing', commit_sha: 'a'.repeat(40), project_id: '123', repository_url: 'https://gitlab.local/repo', manifest_complete: true, member_count: 2, file_count: 1, chunk_count: 2, previous_commit_sha: 'b'.repeat(40), parsed_count: 1, reused_file_count: 3, reused_chunk_count: 6, embedded_chunk_count: 2, reused_vector_count: 6, added_count: 1, changed_count: 2, deleted_count: 1, renamed_count: 1 },
    members: [{ path: 'src/Service.java', status: 'parsed', reason: '', source_file_id: 'file-one', file_version_id: 'version-one' }, { path: 'README.md', status: 'excluded', reason: 'outside selected paths', source_file_id: '', file_version_id: '' }] })
  const component = load(componentPath)
  const host = document.createElement('div')
  document.body.append(host)
  const app = createApp({ render: () => h(component, { result }) })
  app.mount(host)
  try {
    await nextTick()
    assert.ok(host.textContent?.includes('正在建立双索引'))
    assert.ok(host.textContent?.includes('成员清单完整'))
    assert.ok(host.textContent?.includes('实际解析 1'))
    assert.ok(host.textContent?.includes('复用文件 3'))
    assert.ok(host.textContent?.includes('复用向量 6'))
    assert.ok(host.textContent?.includes('当前发布 SHA：' + 'b'.repeat(40)))
    assert.ok(host.textContent?.includes('README.md'))
    assert.equal(Array.from(host.querySelectorAll('button')).find(b => b.textContent?.includes('src/Service.java')), undefined)
    result.snapshot.state = 'published'
    await nextTick()
    assert.ok(host.textContent?.includes('已发布 SHA：' + 'a'.repeat(40)))
    const read = Array.from(host.querySelectorAll<HTMLButtonElement>('button')).find(b => b.textContent?.includes('src/Service.java'))
    assert.ok(read)
    read.click()
    for (let i = 0; i < 4; i++) { await nextTick(); await new Promise<void>(resolve => setImmediate(resolve)) }
    assert.ok(host.textContent?.includes('class Service {}'))
    assert.ok(host.textContent?.includes('原文只读'))
  } finally { app.unmount(); host.remove() }
})

test('failed source runs explain that the last complete publication remains active', async () => {
  const componentPath = fileURLToPath(new URL('./SourceSnapshotRunView.vue', import.meta.url))
  const { descriptor } = parse(readFileSync(componentPath, 'utf8'), { filename: componentPath })
  const compiled = ts.transpileModule(compileScript(descriptor, { id: componentPath, inlineTemplate: true }).content,
    { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
  const module = { exports: {} as any }
  new Function('require', 'module', 'exports', compiled)((name: string) => {
    if (name.endsWith('.vue')) return { __esModule: true, default: {} }
    return require(name)
  }, module, module.exports)
  const component = module.exports.default
  const host = document.createElement('div')
  document.body.append(host)
  const result = reactive({ snapshot: {
    id: 'failed-run', state: 'failed', commit_sha: '', detected_commit_sha: '', target_commit_sha: '',
    project_id: '123', repository_url: 'https://gitlab.local/repo', manifest_complete: false,
    member_count: 0, file_count: 0, chunk_count: 0, previous_commit_sha: 'b'.repeat(40),
    previous_published_at: '2026-09-29T01:02:03Z', error: 'GitLab branch is unavailable'
  }, members: [] })
  const app = createApp({ render: () => h(component, { result }) })
  app.mount(host)
  try {
    await nextTick()
    assert.ok(host.textContent?.includes('发布失败（已保留当前发布）'))
    assert.ok(host.textContent?.includes('当前发布 SHA：' + 'b'.repeat(40)))
    assert.ok(host.textContent?.includes('最后成功发布：2026-09-29T01:02:03Z'))
    assert.ok(host.textContent?.includes('GitLab branch is unavailable'))
  } finally { app.unmount(); host.remove() }
})
